package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ModelLister is implemented by checkers that can enumerate available models
// from the provider's list-models endpoint.
type ModelLister interface {
	// ListModels returns the model IDs advertised by the provider.
	ListModels(ctx context.Context, cfg Config) ([]string, error)
}

// ListModels resolves the checker for a provider type and returns its model
// list. It reports a clear error when the provider does not support listing.
func ListModels(ctx context.Context, c Checker, cfg Config) ([]string, error) {
	ml, ok := c.(ModelLister)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support listing models", c.Type())
	}
	return ml.ListModels(ctx, cfg)
}

// SupportsListModels reports whether the checker can enumerate models.
func SupportsListModels(c Checker) bool {
	_, ok := c.(ModelLister)
	return ok
}

// getJSON issues a GET request with the given auth/header setup and decodes the
// JSON body into v. Non-2xx responses are turned into errors.
func getJSON(ctx context.Context, cfg Config, url string, setHeaders func(*http.Request), v any) error {
	req, err := newReq(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if setHeaders != nil {
		setHeaders(req)
	}
	resp, err := httpClient(cfg).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// dedupeSort returns a de-duplicated, sorted copy of ids (empty entries dropped).
func dedupeSort(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ListModels enumerates models from an OpenAI-compatible /v1/models endpoint.
func (OpenAIChatChecker) ListModels(ctx context.Context, cfg Config) ([]string, error) {
	return openAIListModels(ctx, cfg, "https://api.openai.com")
}

// ListModels enumerates models from the OpenAI Responses base (/v1/models).
func (OpenAIResponseChecker) ListModels(ctx context.Context, cfg Config) ([]string, error) {
	return openAIListModels(ctx, cfg, "https://api.openai.com")
}

// openAIListModels queries the shared OpenAI-compatible /v1/models endpoint.
func openAIListModels(ctx context.Context, cfg Config, def string) ([]string, error) {
	base := normalizeV1(cfg.BaseURL, def)
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	hdr := func(req *http.Request) {
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}
	}
	if err := getJSON(ctx, cfg, base+"/models", hdr, &r); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(r.Data))
	for _, m := range r.Data {
		ids = append(ids, m.ID)
	}
	return dedupeSort(ids), nil
}

// ListModels enumerates models from the Anthropic /v1/models endpoint.
func (AnthropicChecker) ListModels(ctx context.Context, cfg Config) ([]string, error) {
	base := normalizeV1(cfg.BaseURL, "https://api.anthropic.com")
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	resp, err := doAnthropicJSON(ctx, cfg, http.MethodGet, base+"/models?limit=1000", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(r.Data))
	for _, m := range r.Data {
		ids = append(ids, m.ID)
	}
	return dedupeSort(ids), nil
}

// ListModels enumerates models from the Gemini /v1beta/models endpoint.
func (GeminiChecker) ListModels(ctx context.Context, cfg Config) ([]string, error) {
	base := normalizeGemini(cfg.BaseURL)
	u, err := url.Parse(base + "/models")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("pageSize", "1000")
	if cfg.APIKey != "" {
		q.Set("key", cfg.APIKey)
	}
	u.RawQuery = q.Encode()
	var r struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, cfg, u.String(), nil, &r); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(r.Models))
	for _, m := range r.Models {
		// Gemini names look like "models/gemini-1.5-pro"; strip the prefix.
		ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
	}
	return dedupeSort(ids), nil
}
