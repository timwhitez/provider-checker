package checker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"
)

// The synchronous Responses probes (basic/vision/tools/json) send a bounded
// output budget but previously decoded only the feature payload, discarding
// the envelope's completion status. These helpers keep the probes' original
// feature assertions while making the completion state of each HTTP 200
// response visible and diagnosable (issue #13). One request per feature and
// the existing 1 MiB response capture are unchanged.

// responsesSyncBody decodes, in a single pass, the completion envelope plus
// the feature payloads the synchronous Responses probes need.
type responsesSyncBody struct {
	Status            json.RawMessage       `json:"status"`
	IncompleteDetails json.RawMessage       `json:"incomplete_details"`
	Error             json.RawMessage       `json:"error"`
	OutputText        string                `json:"output_text"`
	Output            []responsesSyncOutput `json:"output"`
}

type responsesSyncOutput struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	// Content stays raw so off-spec content on non-message items (which the
	// legacy tools decoder ignored entirely) cannot break the whole decode;
	// it is unmarshaled only for message items inside text().
	Content json.RawMessage `json:"content"`
}

// text mirrors the previous decodeResponseText field preference: the
// output_text shorthand first, then concatenated message/output_text parts.
// Malformed content on a message item is reported as a decode error, matching
// the legacy whole-body decode failure.
func (b *responsesSyncBody) text() (string, error) {
	if b.OutputText != "" {
		return b.OutputText, nil
	}
	var text string
	for _, o := range b.Output {
		if o.Type != "message" {
			continue
		}
		if len(bytes.TrimSpace(o.Content)) == 0 {
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(o.Content, &parts); err != nil {
			return "", err
		}
		for _, c := range parts {
			if c.Type == "output_text" {
				text += c.Text
			}
		}
	}
	return text, nil
}

// toolCall returns the first function_call output item, matching the tools
// probe's existing first-entry selection (only that entry is verified).
func (b *responsesSyncBody) toolCall() *responsesSyncOutput {
	for i := range b.Output {
		if b.Output[i].Type == "function_call" {
			return &b.Output[i]
		}
	}
	return nil
}

func decodeResponsesSyncBody(rd io.Reader) (*responsesSyncBody, error) {
	var b responsesSyncBody
	if err := json.NewDecoder(rd).Decode(&b); err != nil {
		return nil, err
	}
	return &b, nil
}

// rawStringKind classifies one raw JSON field value.
type rawStringKind int

const (
	rawAbsent rawStringKind = iota
	rawNull
	rawEmpty
	rawString
	rawBadType
)

func rawStringValue(raw json.RawMessage) (string, rawStringKind) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", rawAbsent
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return "", rawNull
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return "", rawBadType
	}
	if s == "" {
		return "", rawEmpty
	}
	return s, rawString
}

// responsesClass is the judgment-order classification of one synchronous
// Responses payload: explicit failure first, then incomplete, known
// non-terminal states, and status availability (issue #13).
type responsesClass int

const (
	responsesCompleted responsesClass = iota
	responsesExplicitFailure
	responsesIncomplete
	responsesNonterminal
	responsesStatusUnavailable
	responsesStatusUnrecognized
)

type responsesCompletion struct {
	class  responsesClass
	status string // raw status value for nonterminal/unrecognized/failed states
	reason string // incomplete_details.reason, "" when absent
	errMsg string // rendered provider error for responsesExplicitFailure
}

// classifyResponsesCompletion applies the issue-#13 judgment order. The
// returned error marks parse failures (bad envelope field types), which are
// reported as checker FAILs ahead of any feature evidence.
func classifyResponsesCompletion(b *responsesSyncBody) (responsesCompletion, error) {
	var c responsesCompletion
	status, kind := rawStringValue(b.Status)
	if kind == rawBadType {
		return c, fmt.Errorf("status: expected string, got %s", truncateRunes(string(bytes.TrimSpace(b.Status)), 120))
	}
	errMsg, hasError, err := responsesErrorDetail(b.Error)
	if err != nil {
		return c, err
	}
	// A valid-shaped non-null provider error is an explicit failure regardless
	// of residual output, a completed/incomplete status, or budget exhaustion.
	if hasError {
		c.class = responsesExplicitFailure
		c.errMsg = errMsg
		return c, nil
	}
	switch {
	case kind == rawString && (status == "failed" || status == "cancelled" || status == "canceled"):
		c.class = responsesExplicitFailure
		c.status = status
	case kind == rawString && status == "completed":
		c.class = responsesCompleted
	case kind == rawString && status == "incomplete":
		reason, rerr := responsesIncompleteReason(b.IncompleteDetails)
		if rerr != nil {
			return c, rerr
		}
		c.class = responsesIncomplete
		c.reason = reason
	case kind == rawString && (status == "queued" || status == "in_progress"):
		c.class = responsesNonterminal
		c.status = status
	case kind == rawString:
		c.class = responsesStatusUnrecognized
		c.status = status
	default: // absent, null or empty string: the envelope said nothing
		c.class = responsesStatusUnavailable
	}
	return c, nil
}

// responsesErrorDetail renders a provider error object. A missing or null
// field is not an error; a non-object value is a parse failure; code and
// message inside the object are rendered best-effort.
func responsesErrorDetail(raw json.RawMessage) (detail string, present bool, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false, nil
	}
	var object map[string]json.RawMessage
	if uerr := json.Unmarshal(trimmed, &object); uerr != nil || object == nil {
		return "", false, fmt.Errorf("error: expected object, got %s", truncateRunes(string(trimmed), 120))
	}
	var code, message string
	if field, ok := object["code"]; ok && !bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
		code = renderErrorField(field)
	}
	if field, ok := object["message"]; ok && !bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
		message = renderErrorField(field)
	}
	switch {
	case code != "" && message != "":
		detail = fmt.Sprintf("code=%s message=%q", code, message)
	case message != "":
		detail = fmt.Sprintf("message=%q", message)
	case code != "":
		detail = fmt.Sprintf("code=%s", code)
	default:
		detail = "empty error object " + truncateBytes(string(trimmed), 120)
	}
	return detail, true, nil
}

// renderErrorField best-effort renders one error-object field as a string,
// bounded like failHTTP's embedded body so a pathological provider cannot
// balloon the in-memory result.
func renderErrorField(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return truncateBytes(s, 512)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return truncateBytes(string(raw), 512)
}

// responsesIncompleteReason extracts incomplete_details.reason. A missing or
// null object, or a missing/empty reason, yields "" (reason missing); a
// non-object incomplete_details or non-string reason is a parse failure.
func responsesIncompleteReason(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var object struct {
		Reason json.RawMessage `json:"reason"`
	}
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return "", fmt.Errorf("incomplete_details: expected object, got %s", truncateRunes(string(trimmed), 120))
	}
	reason, kind := rawStringValue(object.Reason)
	if kind == rawBadType {
		return "", fmt.Errorf("incomplete_details.reason: expected string, got %s", truncateRunes(string(bytes.TrimSpace(object.Reason)), 120))
	}
	if kind != rawString {
		return "", nil
	}
	return reason, nil
}

// The GUI log shows the first 80 bytes of Detail/Error. Provider-supplied
// qualifier values are byte-budgeted so the whole prefix, including the
// completion_verified=false tail, stays inside that window.
const (
	incompleteValueBudget   = 80 - len("[incomplete:") - len(" completion_verified=false]")
	unrecognizedValueBudget = 80 - len("[unrecognized_status:") - len(" completion_verified=false]")
)

// completionPrefix returns the compact completion qualifier placed at the
// very start of Detail/Error so the 80-byte log truncation keeps it visible.
// It is "" for a normally completed response.
func completionPrefix(c responsesCompletion) string {
	switch c.class {
	case responsesIncomplete:
		switch {
		case c.reason == "max_output_tokens":
			return "[incomplete:max_output_tokens completion_verified=false probe_budget_exhausted]"
		case c.reason == "content_filter":
			return "[incomplete:content_filter completion_verified=false]"
		case c.reason == "":
			return "[incomplete:reason_missing completion_verified=false]"
		default:
			return "[incomplete:" + truncateBytes(c.reason, incompleteValueBudget) + " completion_verified=false]"
		}
	case responsesNonterminal:
		return "[nonterminal:" + c.status + " completion_verified=false]"
	case responsesStatusUnavailable:
		return "[completion_status_unavailable completion_verified=false]"
	case responsesStatusUnrecognized:
		return "[unrecognized_status:" + truncateBytes(c.status, unrecognizedValueBudget) + " completion_verified=false]"
	}
	return ""
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return s
}

// truncateBytes cuts s to at most limit bytes without splitting a rune, so a
// qualifier built from provider-supplied multibyte values still fits the GUI
// log's byte-based 80-byte window (issue #13 item 5).
func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// responsesDiag records what the probe actually sent: the output cap and the
// explicit reasoning controls. Empty settings are reported as unsent /
// unspecified instead of guessing a backend default (issue #13 item 5).
func responsesDiag(cap int, cfg Config) string {
	effort, mode := "unsent", "unspecified"
	if cfg.ReasoningEffort != "" {
		effort = cfg.ReasoningEffort
	}
	if cfg.ReasoningMode != "" {
		mode = cfg.ReasoningMode
	}
	return fmt.Sprintf("; cap=%d; effort=%s; mode=%s", cap, effort, mode)
}

// responsesEvidence is the original feature-assertion outcome for one probe,
// computed from the same decoded body. ok reports the original PASS
// condition; failMsg is the probe's legacy failure message when !ok.
type responsesEvidence struct {
	ok      bool
	preview string
	failMsg string
}

// responsesProbeOutcome applies the issue-#13 judgment order for one
// synchronous Responses probe. cap is the output budget the probe sent;
// evidence is the original feature assertion over the same decoded body.
func responsesProbeOutcome(name string, start time.Time, resp *http.Response, body *responsesSyncBody, evidence responsesEvidence, cap int, cfg Config) FeatureResult {
	comp, err := classifyResponsesCompletion(body)
	if err != nil {
		return failResponseMsg(name, start, "invalid Responses payload: "+err.Error(), resp)
	}
	prefix := completionPrefix(comp)
	diag := responsesDiag(cap, cfg)
	switch comp.class {
	case responsesExplicitFailure:
		detail := comp.errMsg
		if detail == "" {
			detail = "status=" + comp.status
		}
		return failResponseMsg(name, start, "provider error ("+detail+")"+diag, resp)
	case responsesCompleted:
		// A normally completed response must still pass the original feature
		// assertion; no completion qualifier is added.
		if !evidence.ok {
			return failResponseMsg(name, start, evidence.failMsg, resp)
		}
		return passResponse(name, start, evidence.preview, resp)
	}
	// incomplete / nonterminal / unavailable / unrecognized: valid residual
	// evidence may support a limited PASS, but the response is explicitly not
	// verified as a completed generation; without evidence it is non-PASS.
	if !evidence.ok {
		return failResponseMsg(name, start, prefix+" "+evidence.failMsg+diag, resp)
	}
	return passResponse(name, start, prefix+" "+evidence.preview+diag, resp)
}
