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

func TestReasoningAppliedToEveryProbe(t *testing.T) {
	settings := []struct {
		name, effort, mode string
	}{
		{"empty", "", ""},
		{"effort", "high", ""},
		{"mode", "", "pro"},
		{"both", "high", "pro"},
	}
	for _, typ := range ProviderTypes() {
		c := AllCheckers[typ]
		for _, feature := range c.Supported() {
			for _, setting := range settings {
				t.Run(typ+"/"+feature+"/"+setting.name, func(t *testing.T) {
					var requests atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if r.Method != http.MethodPost {
							t.Errorf("method = %s", r.Method)
						}
						if typ != "gemini" && body["model"] != "requested-alias" {
							t.Errorf("requested model changed: %v", body["model"])
						}
						if typ == "openai-chat" && setting.effort != "" {
							if body["reasoning_effort"] != setting.effort {
								t.Errorf("reasoning_effort = %v, want %s", body["reasoning_effort"], setting.effort)
							}
						} else if _, present := body["reasoning_effort"]; present {
							t.Error("unexpected reasoning_effort")
						}
						if typ == "openai-response" && (setting.effort != "" || setting.mode != "") {
							reasoning, ok := body["reasoning"].(map[string]any)
							if !ok {
								t.Error("missing reasoning object")
							}
							for key, value := range map[string]string{"effort": setting.effort, "mode": setting.mode} {
								got, present := reasoning[key]
								if value == "" && present || value != "" && got != value {
									t.Errorf("reasoning.%s = %v (present=%v), want %q", key, got, present, value)
								}
							}
						} else if _, present := body["reasoning"]; present {
							t.Error("unexpected reasoning object")
						}
						if _, present := body["reasoning_mode"]; present {
							t.Error("unexpected top-level reasoning_mode")
						}
						w.WriteHeader(http.StatusUnprocessableEntity)
						_, _ = w.Write([]byte(`{"model":"declared-upstream","error":"unsupported reasoning parameter"}`))
					}))
					defer server.Close()
					result := c.Test(context.Background(), Config{BaseURL: server.URL, Model: "requested-alias", ReasoningEffort: setting.effort, ReasoningMode: setting.mode}, feature, "hello")
					if requests.Load() != 1 {
						t.Errorf("requests = %d, want one", requests.Load())
					}
					if result.Status != StatusFail || !strings.Contains(result.Error, "HTTP 422:") || !strings.Contains(result.Error, "unsupported reasoning parameter") {
						t.Fatalf("upstream error lost: %+v", result)
					}
					if result.UpstreamResponseModel != "declared-upstream" {
						t.Errorf("upstream model = %q", result.UpstreamResponseModel)
					}
				})
			}
		}
	}
}

// contractProbe returns a real offline HTTP response with a declared upstream
// model so both positive results and assertion failures must retain provenance.
func contractProbe(t *testing.T, typ, feature string, payload map[string]any) FeatureResult {
	t.Helper()
	modelKey := "model"
	if typ == "gemini" {
		modelKey = "modelVersion"
	}
	payload[modelKey] = "declared-upstream"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if feature == "max_tokens" && body["max_tokens"] != float64(probeTokenBudget) {
			t.Errorf("max_tokens = %v, want %d", body["max_tokens"], probeTokenBudget)
		}
		if typ == "openai-response" && feature == "json" {
			format := body["text"].(map[string]any)["format"].(map[string]any)
			if format["strict"] != true || format["type"] != "json_schema" {
				t.Errorf("schema format = %v", format)
			}
			schema, ok := format["schema"].(map[string]any)
			if !ok || schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Errorf("schema object/additionalProperties contract = %v", schema)
			}
			required, ok := schema["required"].([]any)
			if !ok || len(required) != 1 || required[0] != "ok" {
				t.Errorf("schema required = %v, want [ok]", schema["required"])
			}
			properties, ok := schema["properties"].(map[string]any)
			okProperty, isObject := properties["ok"].(map[string]any)
			if !ok || len(properties) != 1 || !isObject || okProperty["type"] != "boolean" {
				t.Errorf("schema properties = %v, want only ok:boolean", schema["properties"])
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	result := AllCheckers[typ].Test(context.Background(), Config{BaseURL: server.URL, Model: "requested-alias"}, feature, "hello")
	if result.UpstreamResponseModel != "declared-upstream" {
		t.Errorf("upstream model lost: %+v", result)
	}
	return result
}

func systemPayload(typ, text string) map[string]any {
	switch typ {
	case "openai-chat":
		return map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}}}}
	case "anthropic":
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
	default:
		return map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": text}}}}}}
	}
}

func TestSystemProbeContract(t *testing.T) {
	for _, typ := range []string{"openai-chat", "anthropic", "gemini"} {
		for _, tc := range []struct {
			name, text string
			want       Status
		}{
			{"exact", "PINEAPPLE", StatusPass},
			{"unicode whitespace", "\u2003\nPINEAPPLE\u00a0", StatusPass},
			{"wrong word", "BANANA", StatusFail},
			{"extra words", "PINEAPPLE is the codeword", StatusFail},
			{"punctuation", "PINEAPPLE.", StatusFail},
			{"empty", "", StatusFail},
			{"whitespace", "\u2003", StatusFail},
		} {
			t.Run(typ+"/"+tc.name, func(t *testing.T) {
				result := contractProbe(t, typ, "system", systemPayload(typ, tc.text))
				if result.Status != tc.want {
					t.Fatalf("result = %+v, want %s", result, tc.want)
				}
			})
		}
		t.Run(typ+"/missing", func(t *testing.T) {
			result := contractProbe(t, typ, "system", map[string]any{})
			if result.Status != StatusFail {
				t.Fatalf("missing response passed: %+v", result)
			}
		})
	}
}

func TestMaxTokensProbeContract(t *testing.T) {
	for _, typ := range []string{"openai-chat", "anthropic"} {
		for _, tc := range []struct {
			name, text string
			tokens     any
			want       Status
		}{
			{"early stop", "1", 1, StatusPass},
			{"at limit", "1 2", 5, StatusPass},
			{"reported zero", "1", 0, StatusPass},
			{"over budget", "1", 100, StatusFail},
			{"negative", "1", -1, StatusFail},
			{"missing usage", "1", nil, StatusFail},
			{"empty content", "", 1, StatusFail},
			{"unicode whitespace", "\u2003\u00a0", 1, StatusFail},
		} {
			t.Run(typ+"/"+tc.name, func(t *testing.T) {
				payload := systemPayload(typ, tc.text)
				usageKey, stopKey, stopValue := "completion_tokens", "finish_reason", "stop"
				if typ == "anthropic" {
					usageKey, stopKey, stopValue = "output_tokens", "stop_reason", "end_turn"
					payload[stopKey] = stopValue
				} else {
					payload["choices"].([]any)[0].(map[string]any)[stopKey] = stopValue
				}
				if tc.tokens != nil {
					payload["usage"] = map[string]any{usageKey: tc.tokens}
				}
				result := contractProbe(t, typ, "max_tokens", payload)
				if result.Status != tc.want {
					t.Fatalf("result = %+v, want %s", result, tc.want)
				}
				if tc.tokens == nil && !strings.Contains(result.Error, "cannot verify") {
					t.Errorf("missing usage not explained: %+v", result)
				}
			})
		}
		t.Run(typ+"/missing response", func(t *testing.T) {
			if result := contractProbe(t, typ, "max_tokens", map[string]any{}); result.Status != StatusFail {
				t.Fatalf("empty response passed: %+v", result)
			}
		})
		for name, usage := range map[string]any{
			"missing statistic": map[string]any{},
			"null usage":        nil,
			"null statistic":    map[string]any{"completion_tokens": nil, "output_tokens": nil},
			"wrong type":        map[string]any{"completion_tokens": "five", "output_tokens": "five"},
		} {
			t.Run(typ+"/"+name, func(t *testing.T) {
				payload := systemPayload(typ, "1")
				payload["usage"] = usage
				result := contractProbe(t, typ, "max_tokens", payload)
				if result.Status != StatusFail {
					t.Fatalf("invalid usage passed: %+v", result)
				}
			})
		}
	}
}

func TestResponsesJSONSchemaContract(t *testing.T) {
	for _, tc := range []struct {
		text string
		want Status
	}{
		{`{"ok":true}`, StatusPass},
		{` { "ok": false } `, StatusPass},
		{`null`, StatusFail},
		{`"valid JSON"`, StatusFail},
		{`[]`, StatusFail},
		{`{}`, StatusFail},
		{`{"ok":"yes"}`, StatusFail},
		{`{"ok":null}`, StatusFail},
		{`{"ok":1}`, StatusFail},
		{`{"ok":true,"extra":1}`, StatusFail},
		{`{"ok":true} trailing`, StatusFail},
		{"", StatusFail},
		{"\u2003", StatusFail},
	} {
		t.Run(fmt.Sprintf("%q", tc.text), func(t *testing.T) {
			payload := map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": tc.text}}}}}
			result := contractProbe(t, "openai-response", "json", payload)
			if result.Status != tc.want {
				t.Fatalf("result = %+v, want %s", result, tc.want)
			}
			if got := okResultAssertion(tc.text) == ""; got != (tc.want == StatusPass) {
				t.Errorf("schema validator accepted = %v", got)
			}
		})
	}
	t.Run("missing output", func(t *testing.T) {
		if result := contractProbe(t, "openai-response", "json", map[string]any{}); result.Status != StatusFail {
			t.Fatalf("missing output passed: %+v", result)
		}
	})
	// output_text is also supplied by some compatible endpoints.
	t.Run("output_text shorthand", func(t *testing.T) {
		if result := contractProbe(t, "openai-response", "json", map[string]any{"output_text": `{"ok":true}`}); result.Status != StatusPass {
			t.Fatalf("valid output_text failed: %+v", result)
		}
	})
}

func TestCompleteTextIsValidated(t *testing.T) {
	cases := []struct {
		typ, feature string
		payload      map[string]any
	}{
		{"openai-chat", "system", map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "PINEAPPLE"}, map[string]any{"type": "text", "text": " extra"}}}}}}},
		{"anthropic", "system", map[string]any{"content": []any{map[string]any{"type": "text", "text": "PINEAPPLE"}, map[string]any{"type": "text", "text": " extra"}}}},
		{"gemini", "system", map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "PINEAPPLE"}, map[string]any{"text": " extra"}}}}}}},
		{"openai-response", "json", map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": `{"ok":true}`}, map[string]any{"type": "output_text", "text": " extra"}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			if result := contractProbe(t, tc.typ, tc.feature, tc.payload); result.Status != StatusFail {
				t.Fatalf("extra text passed: %+v", result)
			}
		})
	}
}

func TestSplitTextCanMeetContract(t *testing.T) {
	cases := []struct {
		typ, feature string
		payload      map[string]any
	}{
		{"openai-chat", "system", map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "PINE"}, map[string]any{"type": "text", "text": "APPLE"}}}}}}},
		{"anthropic", "system", map[string]any{"content": []any{map[string]any{"type": "text", "text": "PINE"}, map[string]any{"type": "text", "text": "APPLE"}}}},
		{"gemini", "system", map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "PINE"}, map[string]any{"text": "APPLE"}}}}}}},
		{"openai-response", "json", map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": `{"ok":`}, map[string]any{"type": "output_text", "text": "true}"}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			if result := contractProbe(t, tc.typ, tc.feature, tc.payload); result.Status != StatusPass {
				t.Fatalf("split valid text failed: %+v", result)
			}
		})
	}
}

func TestSystemMalformedContentPreservesModel(t *testing.T) {
	for _, typ := range []string{"openai-chat", "anthropic", "gemini"} {
		t.Run(typ, func(t *testing.T) {
			payload := systemPayload(typ, "PINEAPPLE")
			switch typ {
			case "openai-chat":
				payload["choices"] = "wrong shape"
			case "anthropic":
				payload["content"] = "wrong shape"
			case "gemini":
				payload["candidates"] = "wrong shape"
			}
			if result := contractProbe(t, typ, "system", payload); result.Status != StatusFail {
				t.Fatalf("malformed content passed: %+v", result)
			}
		})
	}
	t.Run("Responses JSON", func(t *testing.T) {
		if result := contractProbe(t, "openai-response", "json", map[string]any{"output": "wrong shape"}); result.Status != StatusFail {
			t.Fatalf("malformed output passed: %+v", result)
		}
	})
}
