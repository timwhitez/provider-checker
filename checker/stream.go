package checker

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Budgets for this small capability probe, not universal SSE protocol limits.
const (
	streamFrameLimit   = 64 << 10
	streamReadLimit    = 1 << 20
	streamSnippetLimit = 120
)

type streamResult struct {
	Events        int
	Snippet       string
	Model         string
	Partial       bool
	PartialReason string
}

type streamClassifier func(event string, value any) bool

func streamingResult(start time.Time, body io.Reader, classify streamClassifier) FeatureResult {
	result, err := readStream(body, classify)
	if (err != nil && !result.Partial) || result.Events == 0 {
		if err == nil {
			err = fmt.Errorf("no valid streaming events received")
			if result.PartialReason != "" {
				err = fmt.Errorf("no valid streaming events received: %s", result.PartialReason)
			}
		}
		r := fail("Streaming", start, err)
		r.UpstreamResponseModel = result.Model
		return r
	}
	detail := fmt.Sprintf("%d events; %s", result.Events, result.Snippet)
	if result.Partial {
		detail += "; partial: " + result.PartialReason
	}
	return passWithResponseModel("Streaming", start, detail, result.Model)
}

// readStream incrementally dispatches complete SSE frames, independent of Read
// boundaries. Transport errors remain errors; only a caller with validated
// progress may accept them under the existing partial capability policy.
func readStream(r io.Reader, classify streamClassifier) (streamResult, error) {
	// Streaming tracks the model as events arrive; do not also capture a copy of
	// the full stream in the wrapper used by non-streaming JSON decoders.
	if body, ok := r.(*responseModelBody); ok {
		r = body.ReadCloser
	}
	var result streamResult
	var line, data strings.Builder
	var event string
	frameBytes, totalBytes := 0, 0
	pendingCR := false
	dispatch := func() error {
		payload := strings.TrimSpace(data.String())
		failedEvent := streamFailureType(event)
		if payload == "" || payload == "[DONE]" {
			if failedEvent {
				return fmt.Errorf("stream error event: %s", event)
			}
			return nil
		}
		var value any
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			return fmt.Errorf("invalid JSON in SSE frame: %w", err)
		}
		failedPayload := streamPayloadFailure(value)
		if !failedEvent && !failedPayload && !classify(event, value) {
			return nil
		}
		model := responseModelFromJSON([]byte(payload))
		terminal := responseModelTerminalEvent(event, []byte(payload))
		if model != "" && (result.Model == "" || terminal) {
			result.Model = model
		}
		if failedEvent {
			return fmt.Errorf("stream error event: %s", event)
		}
		if failedPayload {
			return fmt.Errorf("stream error/failed payload")
		}
		result.Events++
		if result.Snippet == "" {
			runes := []rune(payload)
			if len(runes) > streamSnippetLimit {
				runes = runes[:streamSnippetLimit]
			}
			result.Snippet = string(runes)
		}
		return nil
	}
	finishLine := func() error {
		text := line.String()
		line.Reset()
		if text == "" {
			err := dispatch()
			data.Reset()
			event = ""
			frameBytes = 0
			return err
		}
		if strings.HasPrefix(text, ":") {
			return nil
		}
		field, value, _ := strings.Cut(text, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		case "event":
			event = value
		}
		return nil
	}
	countFrameByte := func() error {
		frameBytes++
		if frameBytes > streamFrameLimit {
			return fmt.Errorf("SSE frame exceeds %d bytes", streamFrameLimit)
		}
		return nil
	}
	tmp := make([]byte, 4096)
	emptyReads := 0
	for {
		n, readErr := r.Read(tmp)
		if n == 0 && readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				readErr = io.ErrNoProgress
			}
		} else {
			emptyReads = 0
		}
		for _, b := range tmp[:n] {
			totalBytes++
			if totalBytes > streamReadLimit {
				return result, fmt.Errorf("stream exceeds %d bytes", streamReadLimit)
			}
			if pendingCR {
				pendingCR = false
				if b == '\n' {
					if err := countFrameByte(); err != nil {
						return result, err
					}
					if err := finishLine(); err != nil {
						return result, err
					}
					continue
				}
				if err := finishLine(); err != nil {
					return result, err
				}
			}
			if err := countFrameByte(); err != nil {
				return result, err
			}
			switch b {
			case '\r':
				pendingCR = true
			case '\n':
				if err := finishLine(); err != nil {
					return result, err
				}
			default:
				line.WriteByte(b)
			}
		}
		if readErr != nil {
			if pendingCR {
				if err := finishLine(); err != nil {
					return result, err
				}
			}
			if readErr != io.EOF {
				result.Partial = result.Events > 0
				result.PartialReason = readErr.Error()
				return result, readErr
			}
			if frameBytes > 0 {
				result.Partial = result.Events > 0
				result.PartialReason = "truncated SSE frame at EOF"
			}
			return result, nil
		}
	}
}

func streamFailureType(kind string) bool {
	return kind == "error" || kind == "failed" || strings.HasSuffix(kind, ".error") || strings.HasSuffix(kind, ".failed") || kind == "response.cancelled" || kind == "response.canceled"
}

// Check every array item before classifying any as progress (Gemini frames can
// contain arrays). Explicit errors take precedence over otherwise valid data.
func streamPayloadFailure(value any) bool {
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if streamPayloadFailure(item) {
				return true
			}
		}
	}
	if object, ok := value.(map[string]any); ok {
		if errValue, present := object["error"]; present && errValue != nil {
			return true
		}
		kind, _ := object["type"].(string)
		status, _ := object["status"].(string)
		if streamFailureType(kind) || status == "failed" || status == "cancelled" || status == "canceled" {
			return true
		}
		for _, key := range []string{"response", "message"} {
			if streamPayloadFailure(object[key]) {
				return true
			}
		}
	}
	return false
}

// A named SSE event and JSON type may each identify a typed provider event.
// Conflicting identifiers cannot prove the expected protocol contract.
func streamEventType(event string, object map[string]any) string {
	kind, _ := object["type"].(string)
	if event != "" && kind != "" && event != kind {
		return ""
	}
	if kind != "" {
		return kind
	}
	return event
}

func streamObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}
