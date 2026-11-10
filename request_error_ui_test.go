package main

import (
	"context"
	"encoding/csv"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"provider-checker/checker"
	"provider-checker/runner"
)

type uiErrorTransport func(*http.Request) (*http.Response, error)

func (f uiErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestGeminiErrorPresentation(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, key := range []string{"synthetic-secret", "synthetic+/%/&/ secret"} {
		http.DefaultTransport = uiErrorTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("key") != key {
				t.Fatal("request authentication changed")
			}
			return nil, errors.New("connection refused")
		})
		result := (checker.GeminiChecker{}).Test(context.Background(), checker.Config{BaseURL: "https://example.invalid", APIKey: key, Model: "synthetic"}, "basic", "")
		if result.Status != checker.StatusFail {
			t.Fatalf("status = %v, want FAIL", result.Status)
		}
		cell, _ := resultCellText(result, 5)
		outputs := []string{cell, featureDoneLogLine(0, 1, result)}
		path := filepath.Join(t.TempDir(), "results.csv")
		if err := writeResultsCSV(path, []checker.FeatureResult{result}); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(file).ReadAll()
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			outputs = append(outputs, row...)
		}
		for _, output := range outputs {
			for _, secret := range []string{key, url.QueryEscape(key), url.PathEscape(key)} {
				if strings.Contains(output, secret) {
					t.Errorf("output contains request credential: %q", output)
				}
			}
		}
	}
}

func TestGeminiUserCancelStillSkips(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	calls := 0
	http.DefaultTransport = uiErrorTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, ctx.Err()
	})
	var results []checker.FeatureResult
	runner.Run(ctx, checker.GeminiChecker{}, checker.Config{BaseURL: "https://example.invalid", Model: "synthetic", APIKey: "synthetic-secret"}, []string{"basic", "stream"}, "", func(ev runner.Event) {
		if ev.Type == "feature_done" {
			results = append(results, ev.Result)
		}
	})
	if calls != 1 || len(results) != 2 {
		t.Fatalf("calls=%d results=%d", calls, len(results))
	}
	for _, result := range results {
		if result.Status != checker.StatusSkip || result.Error != "cancelled" {
			t.Fatalf("cancel changed: %+v", result)
		}
	}
}
