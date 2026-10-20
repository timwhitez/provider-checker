package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// responsesEnvelopeProbe drives one real synchronous Responses feature
// against an offline fixture returning the exact payload bytes.
func responsesEnvelopeProbe(t *testing.T, feature, payload string, cfg Config) (FeatureResult, int) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	if cfg.BaseURL == "" {
		cfg.BaseURL = server.URL
		cfg.Model = "requested-alias"
	}
	result := AllCheckers["openai-response"].Test(context.Background(), cfg, feature, "hello")
	return result, int(requests.Load())
}

// responsesEnvCase describes one completion-envelope scenario. mutate adjusts
// the valid completed payload; wantErr/wantDetail pin substrings of the
// outcome; banned lists substrings that must not appear.
type responsesEnvCase struct {
	name       string
	mutate     func(base map[string]any)
	want       Status
	wantErr    []string
	wantDetail []string
	banned     []string
}

func runResponsesEnvCase(t *testing.T, feature string, tc responsesEnvCase, cfg Config) {
	t.Helper()
	base := map[string]any{
		"model":  "declared-upstream",
		"status": "completed",
		"output": []any{map[string]any{"type": "message", "content": []any{
			map[string]any{"type": "output_text", "text": "Hello there, this is a valid partial answer."},
		}}},
	}
	switch feature {
	case "tools":
		base["output"] = []any{map[string]any{
			"type": "function_call", "name": "get_weather", "arguments": `{"city":"Tokyo"}`,
		}}
	case "json":
		base["output"] = []any{map[string]any{"type": "message", "content": []any{
			map[string]any{"type": "output_text", "text": `{"ok":true}`},
		}}}
	}
	if tc.mutate != nil {
		tc.mutate(base)
	}
	payload, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	result, requests := responsesEnvelopeProbe(t, feature, string(payload), cfg)
	if requests != 1 {
		t.Errorf("%s: requests = %d, want one", tc.name, requests)
	}
	if result.Status != tc.want {
		t.Fatalf("%s: status = %s (%+v), want %s", tc.name, result.Status, result, tc.want)
	}
	if result.UpstreamResponseModel != "declared-upstream" {
		t.Errorf("%s: upstream model = %q, want declared-upstream", tc.name, result.UpstreamResponseModel)
	}
	if result.Latency <= 0 {
		t.Errorf("%s: latency = %v, want > 0", tc.name, result.Latency)
	}
	for _, want := range tc.wantErr {
		if !strings.Contains(result.Error, want) {
			t.Errorf("%s: error %q misses %q", tc.name, result.Error, want)
		}
	}
	for _, want := range tc.wantDetail {
		if !strings.Contains(result.Detail, want) {
			t.Errorf("%s: detail %q misses %q", tc.name, result.Detail, want)
		}
	}
	for _, banned := range tc.banned {
		if strings.Contains(result.Error, banned) || strings.Contains(result.Detail, banned) {
			t.Errorf("%s: outcome contains banned %q: err=%q detail=%q", tc.name, banned, result.Error, result.Detail)
		}
	}
}

func dropOutput(base map[string]any) {
	delete(base, "output")
}

func reasoningOnlyOutput(base map[string]any) {
	base["output"] = []any{map[string]any{"type": "reasoning", "summary": []any{}}}
}

// TestResponsesCompletionMatrixBasic runs the full completion-state matrix
// through the real basic probe (issue #13 acceptance).
func TestResponsesCompletionMatrixBasic(t *testing.T) {
	cases := []responsesEnvCase{
		{
			name:       "completed valid",
			want:       StatusPass,
			wantDetail: []string{"Hello there"},
			banned:     []string{"completion_verified", "[incomplete", "nonterminal"},
		},
		{
			name:    "failed status with residual output",
			mutate:  func(b map[string]any) { b["status"] = "failed" },
			want:    StatusFail,
			wantErr: []string{"provider error", "status=failed"},
		},
		{
			name:    "cancelled status with residual output",
			mutate:  func(b map[string]any) { b["status"] = "cancelled" },
			want:    StatusFail,
			wantErr: []string{"provider error", "status=cancelled"},
		},
		{
			name:    "single-l canceled spelling is explicit failure",
			mutate:  func(b map[string]any) { b["status"] = "canceled" },
			want:    StatusFail,
			wantErr: []string{"provider error", "status=canceled"},
		},
		{
			name:    "padded status is unrecognized, not failed",
			mutate:  func(b map[string]any) { b["status"] = " failed" },
			want:    StatusFail,
			wantErr: []string{"[unrecognized_status: failed", "completion_verified=false"},
			banned:  []string{"provider error"},
		},
		{
			name: "error object wins over completed and valid output",
			mutate: func(b map[string]any) {
				b["error"] = map[string]any{"code": "server_error", "message": "upstream exploded"}
			},
			want:    StatusFail,
			wantErr: []string{"provider error", "server_error", "upstream exploded"},
			banned:  []string{"probe_budget_exhausted"},
		},
		{
			name: "error object wins over incomplete budget and valid output",
			mutate: func(b map[string]any) {
				b["status"] = "incomplete"
				b["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				b["error"] = map[string]any{"message": "hard failure"}
			},
			want:    StatusFail,
			wantErr: []string{"provider error", "hard failure"},
			banned:  []string{"probe_budget_exhausted"},
		},
		{
			name: "budget exhausted without evidence is non-pass",
			mutate: func(b map[string]any) {
				b["status"] = "incomplete"
				b["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				dropOutput(b)
			},
			want:    StatusFail,
			wantErr: []string{"[incomplete:max_output_tokens", "probe_budget_exhausted", "completion_verified=false", "empty output text", "cap=64", "effort=unsent", "mode=unspecified"},
		},
		{
			name: "budget exhausted with reasoning-only output is non-pass",
			mutate: func(b map[string]any) {
				b["status"] = "incomplete"
				b["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				reasoningOnlyOutput(b)
			},
			want:    StatusFail,
			wantErr: []string{"[incomplete:max_output_tokens", "probe_budget_exhausted", "empty output text"},
		},
		{
			name: "budget exhausted with valid partial is limited pass",
			mutate: func(b map[string]any) {
				b["status"] = "incomplete"
				b["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
			},
			want:       StatusPass,
			wantDetail: []string{"[incomplete:max_output_tokens", "probe_budget_exhausted", "completion_verified=false", "Hello there", "cap=64", "effort=unsent", "mode=unspecified"},
		},
		{
			name: "content_filter keeps its own label",
			mutate: func(b map[string]any) {
				b["status"] = "incomplete"
				b["incomplete_details"] = map[string]any{"reason": "content_filter"}
				dropOutput(b)
			},
			want:    StatusFail,
			wantErr: []string{"[incomplete:content_filter", "completion_verified=false"},
			banned:  []string{"probe_budget_exhausted"},
		},
		{
			name: "missing incomplete reason kept distinct",
			mutate: func(b map[string]any) {
				b["status"] = "incomplete"
				dropOutput(b)
			},
			want:    StatusFail,
			wantErr: []string{"[incomplete:reason_missing", "completion_verified=false"},
			banned:  []string{"probe_budget_exhausted"},
		},
		{
			name: "unknown incomplete reason preserved",
			mutate: func(b map[string]any) {
				b["status"] = "incomplete"
				b["incomplete_details"] = map[string]any{"reason": "turn_limit"}
				dropOutput(b)
			},
			want:    StatusFail,
			wantErr: []string{"[incomplete:turn_limit", "completion_verified=false"},
			banned:  []string{"probe_budget_exhausted"},
		},
		{
			name:    "queued without evidence is non-pass",
			mutate:  func(b map[string]any) { b["status"] = "queued"; dropOutput(b) },
			want:    StatusFail,
			wantErr: []string{"[nonterminal:queued", "completion_verified=false", "empty output text"},
		},
		{
			name:    "in_progress without evidence is non-pass",
			mutate:  func(b map[string]any) { b["status"] = "in_progress"; dropOutput(b) },
			want:    StatusFail,
			wantErr: []string{"[nonterminal:in_progress", "completion_verified=false"},
		},
		{
			name:       "queued with valid output is limited pass",
			mutate:     func(b map[string]any) { b["status"] = "queued" },
			want:       StatusPass,
			wantDetail: []string{"[nonterminal:queued", "completion_verified=false", "Hello there"},
		},
		{
			name:       "missing status with valid output is limited pass",
			mutate:     func(b map[string]any) { delete(b, "status") },
			want:       StatusPass,
			wantDetail: []string{"[completion_status_unavailable", "completion_verified=false", "Hello there"},
		},
		{
			name:    "missing status without evidence is non-pass",
			mutate:  func(b map[string]any) { delete(b, "status"); dropOutput(b) },
			want:    StatusFail,
			wantErr: []string{"[completion_status_unavailable", "empty output text"},
		},
		{
			name:    "null status treated as unavailable",
			mutate:  func(b map[string]any) { b["status"] = nil; dropOutput(b) },
			want:    StatusFail,
			wantErr: []string{"[completion_status_unavailable"},
		},
		{
			name:    "empty status treated as unavailable",
			mutate:  func(b map[string]any) { b["status"] = ""; dropOutput(b) },
			want:    StatusFail,
			wantErr: []string{"[completion_status_unavailable"},
		},
		{
			name:    "unrecognized status preserved",
			mutate:  func(b map[string]any) { b["status"] = "paused"; dropOutput(b) },
			want:    StatusFail,
			wantErr: []string{"[unrecognized_status:paused", "completion_verified=false"},
		},
		{
			name:       "unrecognized status with valid output is limited pass",
			mutate:     func(b map[string]any) { b["status"] = "paused" },
			want:       StatusPass,
			wantDetail: []string{"[unrecognized_status:paused", "completion_verified=false", "Hello there"},
		},
		{
			name:    "non-string status is a parse failure",
			mutate:  func(b map[string]any) { b["status"] = 5 },
			want:    StatusFail,
			wantErr: []string{"invalid Responses payload", "status"},
		},
		{
			name:    "non-object error is a parse failure",
			mutate:  func(b map[string]any) { b["error"] = "boom" },
			want:    StatusFail,
			wantErr: []string{"invalid Responses payload", "error"},
		},
		{
			name:    "non-object incomplete_details is a parse failure",
			mutate:  func(b map[string]any) { b["status"] = "incomplete"; b["incomplete_details"] = "max_output_tokens" },
			want:    StatusFail,
			wantErr: []string{"invalid Responses payload", "incomplete_details"},
		},
		{
			name: "null error with budget exhaustion is not an explicit failure",
			mutate: func(b map[string]any) {
				b["status"] = "incomplete"
				b["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				b["error"] = nil
				dropOutput(b)
			},
			want:    StatusFail,
			wantErr: []string{"probe_budget_exhausted"},
			banned:  []string{"provider error"},
		},
		{
			name:   "null error with completed valid output passes",
			mutate: func(b map[string]any) { b["error"] = nil },
			want:   StatusPass,
			banned: []string{"provider error"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runResponsesEnvCase(t, "basic", tc, Config{}) })
	}
}

// TestResponsesCompletionSubset runs a representative subset of the matrix
// through the vision, tools and json probes (issue #13 acceptance: all four
// branches, no meaningless Cartesian product).
func TestResponsesCompletionSubset(t *testing.T) {
	for _, feature := range []string{"vision", "tools", "json"} {
		subset := []responsesEnvCase{
			{
				name:   "completed valid",
				want:   StatusPass,
				banned: []string{"completion_verified"},
			},
			{
				name:    "failed status with residual evidence",
				mutate:  func(b map[string]any) { b["status"] = "failed" },
				want:    StatusFail,
				wantErr: []string{"provider error", "status=failed"},
			},
			{
				name: "error object wins over completed evidence",
				mutate: func(b map[string]any) {
					b["error"] = map[string]any{"message": "explicit boom"}
				},
				want:    StatusFail,
				wantErr: []string{"provider error", "explicit boom"},
			},
			{
				name: "error object wins over budget exhaustion",
				mutate: func(b map[string]any) {
					b["status"] = "incomplete"
					b["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
					b["error"] = map[string]any{"message": "explicit boom"}
				},
				want:    StatusFail,
				wantErr: []string{"provider error"},
				banned:  []string{"probe_budget_exhausted"},
			},
			{
				name: "budget exhausted without evidence",
				mutate: func(b map[string]any) {
					b["status"] = "incomplete"
					b["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
					dropOutput(b)
				},
				want:    StatusFail,
				wantErr: []string{"probe_budget_exhausted", "completion_verified=false", "cap="},
			},
			{
				name: "budget exhausted with valid partial is limited pass",
				mutate: func(b map[string]any) {
					b["status"] = "incomplete"
					b["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				},
				want:       StatusPass,
				wantDetail: []string{"probe_budget_exhausted", "completion_verified=false"},
			},
			{
				name:       "status unavailable with evidence is limited pass",
				mutate:     func(b map[string]any) { delete(b, "status") },
				want:       StatusPass,
				wantDetail: []string{"completion_status_unavailable", "completion_verified=false"},
			},
		}
		for _, tc := range subset {
			t.Run(feature+"/"+tc.name, func(t *testing.T) { runResponsesEnvCase(t, feature, tc, Config{}) })
		}
	}
}

// TestResponsesCompletedStillAssertsFeature keeps the original feature
// assertions mandatory for completed responses (issue #13 item 4).
func TestResponsesCompletedStillAssertsFeature(t *testing.T) {
	t.Run("json schema violation fails", func(t *testing.T) {
		runResponsesEnvCase(t, "json", responsesEnvCase{
			mutate: func(b map[string]any) {
				b["output"] = []any{map[string]any{"type": "message", "content": []any{
					map[string]any{"type": "output_text", "text": `{"ok":"yes"}`},
				}}}
			},
			want:    StatusFail,
			wantErr: []string{"ok_result schema"},
		}, Config{})
	})
	t.Run("tools wrong function name fails", func(t *testing.T) {
		runResponsesEnvCase(t, "tools", responsesEnvCase{
			mutate: func(b map[string]any) {
				b["output"] = []any{map[string]any{
					"type": "function_call", "name": "wrong_name", "arguments": `{"city":"Tokyo"}`,
				}}
			},
			want:    StatusFail,
			wantErr: []string{"tool-call-local-minimum/v1"},
		}, Config{})
	})
	t.Run("empty output fails", func(t *testing.T) {
		runResponsesEnvCase(t, "vision", responsesEnvCase{
			mutate:  func(b map[string]any) { dropOutput(b) },
			want:    StatusFail,
			wantErr: []string{"empty output text"},
		}, Config{})
	})
}

// TestResponsesExplicitEffortDiag verifies sent reasoning controls are
// recorded on non-completed outcomes (issue #13 item 5).
func TestResponsesExplicitEffortDiag(t *testing.T) {
	runResponsesEnvCase(t, "basic", responsesEnvCase{
		mutate: func(b map[string]any) {
			b["status"] = "incomplete"
			b["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
			dropOutput(b)
		},
		want:    StatusFail,
		wantErr: []string{"cap=64", "effort=high", "mode=pro"},
	}, Config{ReasoningEffort: "high", ReasoningMode: "pro"})
}

// TestResponsesParseFailuresPreserveModel covers decoder error exits: legal
// JSON with a wrongly-shaped output still preserves the independently
// declared response model, corrupted JSON leaves it empty (issue #13 item 6).
func TestResponsesParseFailuresPreserveModel(t *testing.T) {
	for _, feature := range []string{"basic", "vision", "tools", "json"} {
		t.Run(feature+"/wrong output shape", func(t *testing.T) {
			payload := `{"model":"declared-upstream","status":"completed","output":"wrong shape"}`
			result, requests := responsesEnvelopeProbe(t, feature, payload, Config{})
			if requests != 1 {
				t.Errorf("requests = %d, want one", requests)
			}
			if result.Status != StatusFail || !strings.Contains(result.Error, "invalid Responses payload") {
				t.Fatalf("parse failure not reported: %+v", result)
			}
			if result.UpstreamResponseModel != "declared-upstream" {
				t.Errorf("upstream model = %q, want declared-upstream", result.UpstreamResponseModel)
			}
		})
	}
	t.Run("corrupted json leaves model empty", func(t *testing.T) {
		result, _ := responsesEnvelopeProbe(t, "basic", `{"model":"declared-upstream","output":[`, Config{})
		if result.Status != StatusFail {
			t.Fatalf("corrupted json passed: %+v", result)
		}
		if result.UpstreamResponseModel != "" {
			t.Errorf("upstream model = %q, want empty for corrupted capture", result.UpstreamResponseModel)
		}
	})
	t.Run("missing model stays empty and alias is not backfilled", func(t *testing.T) {
		payload := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hello there"}]}]}`
		result, _ := responsesEnvelopeProbe(t, "basic", payload, Config{})
		if result.UpstreamResponseModel != "" {
			t.Errorf("upstream model = %q, want empty when not declared", result.UpstreamResponseModel)
		}
	})
}

// TestResponsesToolsTolerateOffSpecContent keeps the legacy tools decode
// tolerance: an off-spec content field on a function_call item must not break
// the whole payload decode (the legacy tools decoder ignored it).
func TestResponsesToolsTolerateOffSpecContent(t *testing.T) {
	payload := `{"model":"declared-upstream","status":"completed","output":[{"type":"function_call","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}","content":"off-spec"}]}`
	result, requests := responsesEnvelopeProbe(t, "tools", payload, Config{})
	if requests != 1 {
		t.Errorf("requests = %d, want one", requests)
	}
	if result.Status != StatusPass {
		t.Fatalf("off-spec content broke the tools probe: %+v", result)
	}
	if result.UpstreamResponseModel != "declared-upstream" {
		t.Errorf("upstream model = %q", result.UpstreamResponseModel)
	}
}

// TestResponsesLongUnrecognizedStatusInWindow keeps the whole completion
// qualifier within the GUI log's first 80 bytes even for long status values.
func TestResponsesLongUnrecognizedStatusInWindow(t *testing.T) {
	longStatus := strings.Repeat("s", 60)
	payload, err := json.Marshal(map[string]any{
		"model":  "declared-upstream",
		"status": longStatus,
		"output": []any{map[string]any{"type": "message", "content": []any{
			map[string]any{"type": "output_text", "text": "Hello there"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := responsesEnvelopeProbe(t, "basic", string(payload), Config{})
	if result.Status != StatusPass {
		t.Fatalf("limited pass lost: %+v", result)
	}
	logged := result.Detail
	if len(logged) > 80 {
		logged = logged[:80]
	}
	if !strings.Contains(logged, "completion_verified=false") || !strings.Contains(logged, "unrecognized_status:") {
		t.Errorf("80-byte log window %q loses the qualifier", logged)
	}
}

// TestResponsesMultibyteQualifierInWindow pins the byte-budgeted qualifier:
// even 40 CJK runes in an unknown reason keep completion_verified=false
// within the first 80 bytes (issue #13 item 5).
func TestResponsesMultibyteQualifierInWindow(t *testing.T) {
	reason := strings.Repeat("因", 40)
	payload, err := json.Marshal(map[string]any{
		"model":              "declared-upstream",
		"status":             "incomplete",
		"incomplete_details": map[string]any{"reason": reason},
		"output": []any{map[string]any{"type": "message", "content": []any{
			map[string]any{"type": "output_text", "text": "Hello there"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := responsesEnvelopeProbe(t, "basic", string(payload), Config{})
	if result.Status != StatusPass {
		t.Fatalf("limited pass lost: %+v", result)
	}
	logged := result.Detail
	if len(logged) > 80 {
		logged = logged[:80]
	}
	if !strings.Contains(logged, "completion_verified=false") {
		t.Errorf("80-byte log window %q loses the qualifier tail", logged)
	}
}

// TestResponsesPrefixVisibleInLogBudget checks the completion qualifier stays
// within the first 80 bytes of Detail so the GUI log truncation keeps it
// visible, including for long partial output (issue #13 item 5).
func TestResponsesPrefixVisibleInLogBudget(t *testing.T) {
	long := strings.Repeat("A", 300)
	payload, err := json.Marshal(map[string]any{
		"model":              "declared-upstream",
		"status":             "incomplete",
		"incomplete_details": map[string]any{"reason": "max_output_tokens"},
		"output": []any{map[string]any{"type": "message", "content": []any{
			map[string]any{"type": "output_text", "text": long},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := responsesEnvelopeProbe(t, "basic", string(payload), Config{})
	if result.Status != StatusPass {
		t.Fatalf("limited pass lost: %+v", result)
	}
	if !strings.HasPrefix(result.Detail, "[incomplete:max_output_tokens") {
		t.Fatalf("qualifier not at start of detail: %q", result.Detail)
	}
	logged := result.Detail
	if len(logged) > 80 {
		logged = logged[:80]
	}
	for _, want := range []string{"incomplete:max_output_tokens", "completion_verified=false", "probe_budget_exhausted"} {
		if !strings.Contains(logged, want) {
			t.Errorf("80-byte log view %q misses %q", logged, want)
		}
	}
	if result.Latency <= 0 {
		t.Errorf("latency lost: %v", result.Latency)
	}
}
