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
	return httpClient(cfg).Do(req)
}

// readStream reads chunked SSE bytes and returns chunk count + a short snippet.
func readStream(r io.Reader) (int, string, error) {
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
			return got, snippet, nil
		}
	}
	snippet := buf.String()
	if len(snippet) > 120 {
		snippet = snippet[:120] + "..."
	}
	return got, snippet, nil
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
		Name:    name,
		Status:  StatusFail,
		Latency: time.Since(start),
		Error:   fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b))),
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
