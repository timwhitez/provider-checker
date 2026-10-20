package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// toolsPayload builds one provider-shaped tools response carrying a single
// call with the given name and raw arguments. For openai-chat/openai-response
// arguments must be a JSON-encoded string, matching the real wire format; the
// other providers carry structured values (rawJSON embeds arbitrary JSON).
func toolsPayload(typ, name string, arguments json.RawMessage) map[string]any {
	switch typ {
	case "openai-chat":
		return map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"tool_calls": []any{map[string]any{"function": map[string]any{
				"name": name, "arguments": string(arguments),
			}}}}}}}
	case "openai-response":
		return map[string]any{"output": []any{map[string]any{
			"type": "function_call", "name": name, "arguments": string(arguments),
		}}}
	case "anthropic":
		return map[string]any{"content": []any{map[string]any{
			"type": "tool_use", "name": name, "input": arguments,
		}}}
	default: // gemini
		return map[string]any{"candidates": []any{map[string]any{"content": map[string]any{
			"parts": []any{map[string]any{"functionCall": map[string]any{
				"name": name, "args": arguments,
			}}}}}}}
	}
}

// toolsContractProbe drives the real tools feature of one provider type
// against an offline fixture, returning the FeatureResult and the exact
// request body the checker sent.
func toolsContractProbe(t *testing.T, typ string, payload map[string]any) (FeatureResult, map[string]any) {
	t.Helper()
	modelKey := "model"
	if typ == "gemini" {
		modelKey = "modelVersion"
	}
	payload[modelKey] = "declared-upstream"
	var requests atomic.Int32
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()
	result := AllCheckers[typ].Test(context.Background(), Config{BaseURL: server.URL, Model: "requested-alias"}, "tools", "hello")
	if requests.Load() != 1 {
		t.Errorf("%s: requests = %d, want one", typ, requests.Load())
	}
	if result.UpstreamResponseModel != "declared-upstream" {
		t.Errorf("%s: upstream model lost: %+v", typ, result)
	}
	return result, sent
}

// TestToolCallLocalMinimumContract runs the selected-call assertion through
// every adapter's real tools feature (issue #10).
func TestToolCallLocalMinimumContract(t *testing.T) {
	cases := []struct {
		name      string
		callName  string
		arguments string
		want      Status
	}{
		{"city string", "get_weather", `{"city":"Tokyo"}`, StatusPass},
		{"empty city string", "get_weather", `{"city":""}`, StatusPass},
		{"other city", "get_weather", `{"city":"Paris"}`, StatusPass},
		{"extra property is a local positive", "get_weather", `{"city":"Tokyo","extra":1}`, StatusPass},
		{"wrong name", "wrong_name", `{"city":"Tokyo"}`, StatusFail},
		{"missing name", "", `{"city":"Tokyo"}`, StatusFail},
		{"null arguments", "get_weather", `null`, StatusFail},
		{"array arguments", "get_weather", `[1,2]`, StatusFail},
		{"missing city", "get_weather", `{"other":1}`, StatusFail},
		{"numeric city", "get_weather", `{"city":7}`, StatusFail},
		{"non-object arguments", "get_weather", `"just a string"`, StatusFail},
	}
	for _, typ := range ProviderTypes() {
		for _, tc := range cases {
			t.Run(typ+"/"+tc.name, func(t *testing.T) {
				result, _ := toolsContractProbe(t, typ, toolsPayload(typ, tc.callName, json.RawMessage(tc.arguments)))
				if result.Status != tc.want {
					t.Fatalf("%s: result = %+v, want %s", typ, result, tc.want)
				}
				if tc.want == StatusPass {
					if !strings.Contains(result.Detail, "tool-call-local-minimum/v1") ||
						!strings.Contains(result.Detail, "literal-local-minimum") ||
						!strings.Contains(result.Detail, "backend_effective_schema=not_checked") {
						t.Errorf("%s: PASS detail lacks contract annotation: %q", typ, result.Detail)
					}
				} else if !strings.Contains(result.Error, "tool-call-local-minimum/v1") ||
					!strings.Contains(result.Error, "literal-local-minimum") ||
					!strings.Contains(result.Error, "backend_effective_schema=not_checked") {
					t.Errorf("%s: FAIL error lacks contract annotation: %q", typ, result.Error)
				}
			})
		}
		// Syntactically invalid arguments only exist for string-encoded forms.
		if typ == "openai-chat" || typ == "openai-response" {
			t.Run(typ+"/invalid json arguments", func(t *testing.T) {
				payload := toolsPayload(typ, "get_weather", json.RawMessage(`{"city":"Tokyo"}`))
				switch typ {
				case "openai-chat":
					payload["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"] = `{invalid`
				case "openai-response":
					payload["output"].([]any)[0].(map[string]any)["arguments"] = `{invalid`
				}
				result, _ := toolsContractProbe(t, typ, payload)
				if result.Status != StatusFail {
					t.Fatalf("%s: invalid arguments passed: %+v", typ, result)
				}
			})
		}
		t.Run(typ+"/no tool entry", func(t *testing.T) {
			payload := map[string]any{}
			switch typ {
			case "openai-chat":
				payload["choices"] = []any{map[string]any{"message": map[string]any{"content": "it is sunny"}}}
			case "openai-response":
				payload["output"] = []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "it is sunny"}}}}
			case "anthropic":
				payload["content"] = []any{map[string]any{"type": "text", "text": "it is sunny"}}
			default:
				payload["candidates"] = []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "it is sunny"}}}}}
			}
			result, _ := toolsContractProbe(t, typ, payload)
			if result.Status != StatusFail {
				t.Fatalf("%s: missing tool entry passed: %+v", typ, result)
			}
			if !strings.Contains(result.Error, "tool-call-local-minimum/v1") ||
				!strings.Contains(result.Error, "literal-local-minimum") ||
				!strings.Contains(result.Error, "backend_effective_schema=not_checked") {
				t.Errorf("%s: no-entry failure lacks contract annotation: %q", typ, result.Error)
			}
		})
	}
}

// TestToolCallAssertionUnit pins the shared helper on the local-minimum
// boundary: extra properties, other cities and empty strings are valid; the
// contract never claims backend-effective schema or strict conformance.
func TestToolCallAssertionUnit(t *testing.T) {
	cases := []struct {
		name, args string
		ok         bool
	}{
		{"tokyo", `{"city":"Tokyo"}`, true},
		{"empty city", `{"city":""}`, true},
		{"extra property", `{"city":"Tokyo","extra":1}`, true},
		{"whitespace padded", `  {"city":"Tokyo"}  `, true},
		{"wrong name args", `{"city":true}`, false},
		{"numeric city", `{"city":7}`, false},
		{"null city", `{"city":null}`, false},
		{"missing city", `{"other":1}`, false},
		{"empty object", `{}`, false},
		{"null args", `null`, false},
		{"array args", `[]`, false},
		{"string args", `"x"`, false},
		{"number args", `5`, false},
		{"invalid json", `{invalid`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.args, func(t *testing.T) {
			if got := toolCallAssertion("get_weather", tc.args); (got == "") != tc.ok {
				t.Fatalf("toolCallAssertion(get_weather, %s) = %q, ok=%v", tc.args, got, tc.ok)
			}
			if got := toolCallAssertion("other_fn", `{"city":"Tokyo"}`); got == "" {
				t.Fatal("wrong function name accepted")
			}
		})
	}
}

// findToolParameters navigates each transport's tool declaration to the
// JSON-schema parameters object declared for get_weather.
func findToolParameters(typ string, sent map[string]any) (map[string]any, bool) {
	switch typ {
	case "openai-chat":
		tools, _ := sent["tools"].([]any)
		if len(tools) == 0 {
			return nil, false
		}
		fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		return params, fn["name"] == "get_weather"
	case "openai-response":
		tools, _ := sent["tools"].([]any)
		if len(tools) == 0 {
			return nil, false
		}
		tool, _ := tools[0].(map[string]any)
		params, _ := tool["parameters"].(map[string]any)
		return params, tool["name"] == "get_weather"
	case "anthropic":
		tools, _ := sent["tools"].([]any)
		if len(tools) == 0 {
			return nil, false
		}
		tool, _ := tools[0].(map[string]any)
		params, _ := tool["input_schema"].(map[string]any)
		return params, tool["name"] == "get_weather"
	default: // gemini
		tools, _ := sent["tools"].([]any)
		if len(tools) == 0 {
			return nil, false
		}
		decls, _ := tools[0].(map[string]any)["function_declarations"].([]any)
		if len(decls) == 0 {
			return nil, false
		}
		decl, _ := decls[0].(map[string]any)
		params, _ := decl["parameters"].(map[string]any)
		return params, decl["name"] == "get_weather"
	}
}

// TestToolsProbeRequestShapeUnchanged verifies the fix added no request-side
// constraints: same tool declaration, city schema and transport wrappers;
// Responses keeps omitting strict and additionalProperties (issue #10 item 6).
func TestToolsProbeRequestShapeUnchanged(t *testing.T) {
	for _, typ := range ProviderTypes() {
		t.Run(typ, func(t *testing.T) {
			result, sent := toolsContractProbe(t, typ, toolsPayload(typ, "get_weather", json.RawMessage(`{"city":"Tokyo"}`)))
			if result.Status != StatusPass {
				t.Fatalf("baseline call failed: %+v", result)
			}
			params, named := findToolParameters(typ, sent)
			if !named || params == nil {
				t.Fatalf("%s: get_weather declaration missing from request: %v", typ, sent["tools"])
			}
			properties, _ := params["properties"].(map[string]any)
			city, _ := properties["city"].(map[string]any)
			if city["type"] != "string" {
				t.Errorf("%s: city property = %v, want type string", typ, city)
			}
			required, _ := params["required"].([]any)
			if len(required) != 1 || required[0] != "city" {
				t.Errorf("%s: required = %v, want [city]", typ, required)
			}
			if typ == "openai-response" {
				tool, _ := sent["tools"].([]any)[0].(map[string]any)
				if _, hasStrict := tool["strict"]; hasStrict {
					t.Errorf("%s: strict must stay omitted, got %v", typ, tool["strict"])
				}
				if _, hasAP := params["additionalProperties"]; hasAP {
					t.Errorf("%s: additionalProperties must stay omitted, got %v", typ, params["additionalProperties"])
				}
			}
			if typ == "openai-chat" && sent["tool_choice"] != "auto" {
				t.Errorf("%s: tool_choice = %v, want auto", typ, sent["tool_choice"])
			}
			if fmt.Sprint(sent["model"]) == "" && typ != "gemini" {
				t.Errorf("%s: model missing from request", typ)
			}
		})
	}
}

// TestToolsHTTPErrorPreserved keeps the legacy HTTP failure contract on the
// tools feature: the provider's 4xx body is retained verbatim.
func TestToolsHTTPErrorPreserved(t *testing.T) {
	for _, typ := range ProviderTypes() {
		t.Run(typ, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"model":"declared-upstream","error":"tools not supported here"}`))
			}))
			defer server.Close()
			result := AllCheckers[typ].Test(context.Background(), Config{BaseURL: server.URL, Model: "requested-alias"}, "tools", "hello")
			if requests.Load() != 1 {
				t.Errorf("requests = %d, want one", requests.Load())
			}
			if result.Status != StatusFail || !strings.Contains(result.Error, "HTTP 400:") || !strings.Contains(result.Error, "tools not supported here") {
				t.Fatalf("%s: HTTP error lost: %+v", typ, result)
			}
			if result.UpstreamResponseModel != "declared-upstream" {
				t.Errorf("%s: upstream model = %q", typ, result.UpstreamResponseModel)
			}
		})
	}
}
