package checker

import (
	"encoding/json"
	"fmt"
	"strings"
)

const probeTokenBudget = 5

// systemAssertion checks the complete textual answer, ignoring surrounding
// Unicode whitespace but not additional words or punctuation.
func systemAssertion(text string) string {
	if strings.TrimSpace(text) != "PINEAPPLE" {
		return fmt.Sprintf("system instruction not followed: expected PINEAPPLE, got %q", text)
	}
	return ""
}

// tokenBudgetAssertion uses reported usage rather than guessing from text size.
// A normal early stop is valid; reaching the limit is not required.
func tokenBudgetAssertion(text string, tokens *int) string {
	if strings.TrimSpace(text) == "" {
		return "empty output content; cannot verify token limit"
	}
	if tokens == nil {
		return "missing output token usage; cannot verify token limit"
	}
	if *tokens < 0 || *tokens > probeTokenBudget {
		return fmt.Sprintf("reported output tokens %d outside requested budget 0..%d", *tokens, probeTokenBudget)
	}
	return ""
}

// okResultAssertion validates the specific schema sent by the Responses probe.
// General JSON syntax checks elsewhere intentionally keep their broader meaning.
func okResultAssertion(text string) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &object); err != nil {
		return "response does not match ok_result schema: expected a JSON object with only boolean ok"
	}
	value, present := object["ok"]
	if !present || len(object) != 1 {
		return "response does not match ok_result schema: required boolean ok and no additional properties"
	}
	// Unmarshaling null into a bool succeeds with its zero value, so explicitly
	// reject it as well as all other non-boolean JSON values.
	if string(value) != "true" && string(value) != "false" {
		return "response does not match ok_result schema: ok must be a boolean"
	}
	return ""
}
