package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Shared tiny solid-red PNG (1x1) for vision probes. Avoids relying on an
// external image host that providers may not be able to fetch.
const redPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// redPNGDataURI is the same image as a data URI for OpenAI-style image_url fields.
const redPNGDataURI = "data:image/png;base64," + redPNGBase64

// postJSON marshals body and POSTs it as application/json.
func postJSON(ctx context.Context, cfg Config, url string, body any, setAuth func(*http.Request)) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := newReq(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if setAuth != nil {
		setAuth(req)
	}
	resp, err := httpClient(cfg).Do(req)
	return trackResponseModel(resp), err
}

// responseModelBody retains the bytes consumed by a decoder so the raw model
// identifier declared by the upstream response remains available afterwards.
// Capability probes use small output limits, and the captured data stays local
// to one request/result.
type responseModelBody struct {
	io.ReadCloser
	buf bytes.Buffer
}

const responseModelCaptureLimit = 1 << 20

func (b *responseModelBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		remaining := responseModelCaptureLimit - b.buf.Len()
		if remaining > 0 {
			captureN := n
			if remaining < captureN {
				captureN = remaining
			}
			_, _ = b.buf.Write(p[:captureN])
		}
	}
	return n, err
}

func (b *responseModelBody) model() string {
	if b == nil {
		return ""
	}
	return responseModelFromJSON(b.buf.Bytes())
}

func trackResponseModel(resp *http.Response) *http.Response {
	if resp != nil && resp.Body != nil {
		resp.Body = &responseModelBody{ReadCloser: resp.Body}
	}
	return resp
}

// upstreamResponseModel returns only the model declared by the upstream
// response. It deliberately never falls back to Config.Model, because that is
// merely the client-requested alias and may differ from the concrete route.
func upstreamResponseModel(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	if body, ok := resp.Body.(*responseModelBody); ok {
		return body.model()
	}
	return ""
}

// passResponse attaches the model declared by a fully-consumed JSON response.
func passResponse(name string, start time.Time, detail string, resp *http.Response) FeatureResult {
	r := pass(name, start, detail)
	r.UpstreamResponseModel = upstreamResponseModel(resp)
	return r
}

func passWithResponseModel(name string, start time.Time, detail, model string) FeatureResult {
	r := pass(name, start, detail)
	r.UpstreamResponseModel = model
	return r
}

// failResponseMsg retains a declared upstream model even when a capability
// assertion fails after receiving an otherwise valid response.
func failResponseMsg(name string, start time.Time, msg string, resp *http.Response) FeatureResult {
	r := failMsg(name, start, msg)
	r.UpstreamResponseModel = upstreamResponseModel(resp)
	return r
}

// readStream reads chunked SSE bytes and returns chunk count, a short snippet,
// and the first model identifier declared by a stream event.
func readStream(r io.Reader) (int, string, string, error) {
	got := 0
	tmp := make([]byte, 1024)
	var buf strings.Builder
	for {
		n, rerr := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			got++
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			// Partial stream still counts as progress for capability probes.
			snippet := buf.String()
			if len(snippet) > 120 {
				snippet = snippet[:120] + "..."
			}
			return got, snippet, responseModelFromStream([]byte(buf.String())), nil
		}
	}
	snippet := buf.String()
	if len(snippet) > 120 {
		snippet = snippet[:120] + "..."
	}
	return got, snippet, responseModelFromStream([]byte(buf.String())), nil
}

// normalizeV1 ensures the base URL ends with /v1, defaulting when empty.
func normalizeV1(baseURL, def string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = def
	}
	if !strings.HasSuffix(base, "/v1") && !strings.Contains(base, "/v1/") {
		base = base + "/v1"
	}
	return base
}

func fail(name string, start time.Time, err error) FeatureResult {
	return FeatureResult{Name: name, Status: StatusFail, Latency: time.Since(start), Error: err.Error()}
}

func failMsg(name string, start time.Time, msg string) FeatureResult {
	return FeatureResult{Name: name, Status: StatusFail, Latency: time.Since(start), Error: msg}
}

func failHTTP(name string, start time.Time, resp *http.Response) FeatureResult {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return FeatureResult{
		Name:                  name,
		Status:                StatusFail,
		Latency:               time.Since(start),
		UpstreamResponseModel: upstreamResponseModel(resp),
		Error:                 fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b))),
	}
}

func pass(name string, start time.Time, detail string) FeatureResult {
	return FeatureResult{Name: name, Status: StatusPass, Latency: time.Since(start), Detail: detail}
}

func skip(feature string) FeatureResult {
	return FeatureResult{Name: feature, Status: StatusSkip}
}

// looksLikeJSON reports whether s is a JSON object or array (best-effort probe).
func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	var v any
	return json.Unmarshal([]byte(s), &v) == nil
}

// responseModelFromJSON extracts the raw model identifier from the common
// OpenAI, Anthropic, and Gemini response shapes. The field is intentionally
// response-derived: no request-side model value is accepted as a fallback.
func responseModelFromJSON(payload []byte) string {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	for _, path := range [][]string{
		{"response", "model"},
		{"message", "model"},
		{"model"},
		{"modelVersion"},
		{"response", "modelVersion"},
		{"response", "response", "modelVersion"},
	} {
		if model := responseModelAtPath(value, path...); model != "" {
			return model
		}
	}
	return ""
}

func responseModelAtPath(value any, path ...string) string {
	if len(path) == 0 {
		if model, ok := value.(string); ok {
			return normalizeResponseModel(model)
		}
		return ""
	}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if model := responseModelAtPath(item, path...); model != "" {
				return model
			}
		}
		return ""
	}
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return responseModelAtPath(object[path[0]], path[1:]...)
}

func normalizeResponseModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	runes := []rune(model)
	if len(runes) > 200 {
		return string(runes[:200])
	}
	return model
}

// responseModelFromStream scans SSE data frames for response model
// declarations. A terminal OpenAI Responses event wins over an earlier model
// declaration, matching the upstream's final routing decision. It also accepts
// JSON array frames used by some Gemini streaming endpoints.
func responseModelFromStream(stream []byte) string {
	first := ""
	terminal := ""
	for _, frame := range strings.Split(string(stream), "\n\n") {
		var data []string
		eventType := ""
		for _, line := range strings.Split(frame, "\n") {
			if value, ok := strings.CutPrefix(line, "event:"); ok {
				eventType = strings.TrimSpace(value)
			}
			if value, ok := strings.CutPrefix(line, "data:"); ok {
				value = strings.TrimSpace(value)
				if value != "" && value != "[DONE]" {
					data = append(data, value)
				}
			}
		}
		payload := []byte(strings.Join(data, "\n"))
		model := responseModelFromJSON(payload)
		if model == "" {
			continue
		}
		if first == "" {
			first = model
		}
		if responseModelTerminalEvent(eventType, payload) {
			terminal = model
		}
	}
	if terminal != "" {
		return terminal
	}
	return first
}

func responseModelTerminalEvent(eventType string, payload []byte) bool {
	if eventType == "" {
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(payload, &envelope) == nil {
			eventType = envelope.Type
		}
	}
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}
