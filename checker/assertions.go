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

// toolCallContractVersion identifies the narrow, versioned local minimum the
// tools probes assert: the selected call names the function literally declared
// by this probe run and its arguments form a JSON object with a string `city`.
// It is a literal declaration-side contract: it is not the provider's
// backend-effective schema and implies nothing about strict-mode conformance.
const toolCallContractVersion = "tool-call-local-minimum/v1"

// toolCallContractNote delimits what a tools result actually verified: one
// single-turn call emission against the local minimum above. Extra argument
// properties, other cities and an empty city string are valid under this
// local contract even where a backend-effective schema would reject them.
const toolCallContractNote = "[tool-call-local-minimum/v1 literal-local-minimum backend_effective_schema=not_checked]"

// toolCallAssertion checks the selected tool call against the literal local
// minimum declared by the probes (get_weather with a string city argument).
// The returned message is empty when the call satisfies the contract; it must
// never be read as a claim that the provider lacks tool support entirely.
func toolCallAssertion(name, argsJSON string) string {
	if name != "get_weather" {
		return fmt.Sprintf("response did not meet this probe's tool-call contract %s: function name %q, want \"get_weather\"", toolCallContractNote, name)
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args == nil {
		return fmt.Sprintf("response did not meet this probe's tool-call contract %s: arguments are not a JSON object: %s", toolCallContractNote, argsJSON)
	}
	city, present := args["city"]
	if !present {
		return fmt.Sprintf("response did not meet this probe's tool-call contract %s: arguments miss required property \"city\" (string): %s", toolCallContractNote, argsJSON)
	}
	var cityValue string
	// Unmarshaling null into a string succeeds with its zero value, so reject
	// it explicitly along with every other non-string JSON value.
	if string(city) == "null" || json.Unmarshal(city, &cityValue) != nil {
		return fmt.Sprintf("response did not meet this probe's tool-call contract %s: property \"city\" must be a string: %s", toolCallContractNote, argsJSON)
	}
	return ""
}
