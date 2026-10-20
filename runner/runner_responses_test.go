package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"provider-checker/checker"
)

// responsesFixture serves one fixed JSON payload for every probe request and
// counts requests so tests can pin the one-request-per-feature contract.
func responsesFixture(t *testing.T, payload string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

// TestRunSurfacesCompletionDiagnostics drives the real openai-response checker
// through Run and verifies the feature_done result keeps the completion
// qualifier inside the GUI log's 80-byte window, together with latency and
// the independently declared upstream model (issue #13 acceptance).
func TestRunSurfacesCompletionDiagnostics(t *testing.T) {
	long := strings.Repeat("partial answer ", 40)
	payload := `{"model":"declared-upstream","status":"incomplete",` +
		`"incomplete_details":{"reason":"max_output_tokens"},` +
		`"output":[{"type":"message","content":[{"type":"output_text","text":"` + long + `"}]}]}`
	server, requests := responsesFixture(t, payload)

	var results []checker.FeatureResult
	Run(context.Background(), checker.AllCheckers["openai-response"],
		checker.Config{BaseURL: server.URL, Model: "requested-alias"},
		[]string{"basic"}, "hello", func(ev Event) {
			if ev.Type == "feature_done" {
				results = append(results, ev.Result)
			}
		})

	if len(results) != 1 {
		t.Fatalf("feature_done results = %d, want one", len(results))
	}
	if requests.Load() != 1 {
		t.Errorf("requests = %d, want one", requests.Load())
	}
	r := results[0]
	if r.Status != checker.StatusPass {
		t.Fatalf("limited pass lost: %+v", r)
	}
	if !strings.HasPrefix(r.Detail, "[incomplete:max_output_tokens") {
		t.Fatalf("qualifier not leading detail: %q", r.Detail)
	}
	logged := r.Detail
	if len(logged) > 80 {
		logged = logged[:80]
	}
	for _, want := range []string{"incomplete:max_output_tokens", "completion_verified=false", "probe_budget_exhausted"} {
		if !strings.Contains(logged, want) {
			t.Errorf("80-byte log window %q misses %q", logged, want)
		}
	}
	if r.Latency <= 0 {
		t.Errorf("latency lost: %v", r.Latency)
	}
	if r.UpstreamResponseModel != "declared-upstream" {
		t.Errorf("upstream model = %q, want declared-upstream", r.UpstreamResponseModel)
	}
}

// TestRunKeepsPreCancelResultsAndSkipsRest pins the local cancellation rules
// with the real checker: a result already emitted before cancellation keeps
// its status (PASS stays PASS, an upstream error stays an error), and the
// remaining features are SKIP/cancelled (issue #13 acceptance).
func TestRunKeepsPreCancelResultsAndSkipsRest(t *testing.T) {
	completed := `{"model":"declared-upstream","status":"completed",` +
		`"output":[{"type":"message","content":[{"type":"output_text","text":"Hello there"}]}]}`
	explicitError := `{"model":"declared-upstream","status":"completed",` +
		`"error":{"code":"server_error","message":"upstream exploded"},` +
		`"output":[{"type":"message","content":[{"type":"output_text","text":"residual"}]}]}`
	var request int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&request, 1)
		payload := completed
		if n == 2 {
			payload = explicitError
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var statuses []checker.Status
	var details []string
	Run(ctx, checker.AllCheckers["openai-response"],
		checker.Config{BaseURL: server.URL, Model: "requested-alias"},
		[]string{"basic", "vision", "json"}, "hello", func(ev Event) {
			if ev.Type != "feature_done" {
				return
			}
			statuses = append(statuses, ev.Result.Status)
			details = append(details, ev.Result.Detail+"\x00"+ev.Result.Error)
			// Cancel once the second (erroring) feature has been reported.
			if len(statuses) == 2 {
				cancel()
			}
		})

	if len(statuses) != 3 {
		t.Fatalf("feature_done count = %d, want 3 (%v)", len(statuses), statuses)
	}
	if statuses[0] != checker.StatusPass {
		t.Errorf("first feature = %v, want PASS preserved", statuses[0])
	}
	if statuses[1] != checker.StatusFail || !strings.Contains(details[1], "upstream exploded") {
		t.Errorf("pre-cancel error overwritten: status=%v detail=%q", statuses[1], details[1])
	}
	if statuses[2] != checker.StatusSkip || !strings.Contains(details[2], "cancelled") {
		t.Errorf("post-cancel feature = %v (%q), want SKIP/cancelled", statuses[2], details[2])
	}
}
