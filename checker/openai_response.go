package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAIResponseChecker tests the OpenAI /v1/responses endpoint (Responses API).
type OpenAIResponseChecker struct{}

func (OpenAIResponseChecker) Type() string { return "openai-response" }

func (OpenAIResponseChecker) Supported() []string {
	return []string{"basic", "stream", "vision", "tools", "json"}
}

func (c OpenAIResponseChecker) Test(ctx context.Context, cfg Config, feature, prompt string) FeatureResult {
	if prompt == "" {
		prompt = "Say hello in one short sentence."
	}
	start := time.Now()
	base := normalizeV1(cfg.BaseURL, "https://api.openai.com")
	hdr := func(req *http.Request) {
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}
	}

	switch feature {
	case "basic":
		body := map[string]any{
			"model":             cfg.Model,
			"input":             prompt,
			"max_output_tokens": 64,
		}
		applyResponsesReasoning(body, cfg)
		resp, err := postJSON(ctx, cfg, base+"/responses", body, hdr)
		if err != nil {
			return fail("Basic", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Basic", start, resp)
		}
		text, err := decodeResponseText(resp.Body)
		if err != nil {
			return fail("Basic", start, err)
		}
		if text == "" {
			return failMsg("Basic", start, "empty output text")
		}
		return pass("Basic", start, text)

	case "stream":
		body := map[string]any{
			"model":             cfg.Model,
			"input":             prompt,
			"max_output_tokens": 32,
			"stream":            true,
		}
		resp, err := postJSON(ctx, cfg, base+"/responses", body, hdr)
		if err != nil {
			return fail("Streaming", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Streaming", start, resp)
		}
		n, snippet, err := readStream(resp.Body)
		if err != nil {
			return fail("Streaming", start, err)
		}
		if n == 0 {
			return failMsg("Streaming", start, "no chunks received")
		}
		return pass("Streaming", start, fmt.Sprintf("%d chunks; %s", n, snippet))

	case "vision":
		body := map[string]any{
			"model": cfg.Model,
			"input": []map[string]any{
				{"role": "user", "content": []map[string]any{
					{"type": "input_text", "text": "What color is this image? Reply with one word."},
					{"type": "input_image", "image_url": redPNGDataURI},
				}},
			},
			"max_output_tokens": 16,
		}
		resp, err := postJSON(ctx, cfg, base+"/responses", body, hdr)
		if err != nil {
			return fail("Vision", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Vision", start, resp)
		}
		text, err := decodeResponseText(resp.Body)
		if err != nil {
			return fail("Vision", start, err)
		}
		if text == "" {
			return failMsg("Vision", start, "empty output text")
		}
		return pass("Vision", start, text)

	case "tools":
		body := map[string]any{
			"model": cfg.Model,
			"input": "What is the weather in Tokyo? Use the provided tool.",
			"tools": []map[string]any{
				{"type": "function", "name": "get_weather", "description": "Get current weather for a city",
					"parameters": map[string]any{
						"type":       "object",
						"properties": map[string]any{"city": map[string]string{"type": "string"}},
						"required":   []string{"city"},
					}},
			},
			"max_output_tokens": 128,
		}
		resp, err := postJSON(ctx, cfg, base+"/responses", body, hdr)
		if err != nil {
			return fail("Tool Calling", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Tool Calling", start, resp)
		}
		var r struct {
			Output []struct {
				Type string `json:"type"`
				Name string `json:"name"`
				Args string `json:"arguments"`
			} `json:"output"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return fail("Tool Calling", start, err)
		}
		for _, o := range r.Output {
			if o.Type == "function_call" {
				return pass("Tool Calling", start, fmt.Sprintf("%s(%s)", o.Name, o.Args))
			}
		}
		return failMsg("Tool Calling", start, "no function_call in output")

	case "json":
		body := map[string]any{
			"model": cfg.Model,
			"input": "Return a JSON object with a key 'ok' set to true.",
			"text": map[string]any{
				"format": map[string]any{
					"type": "json_schema",
					"name": "ok_result",
					"schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"ok": map[string]string{"type": "boolean"},
						},
						"required":             []string{"ok"},
						"additionalProperties": false,
					},
				},
			},
			"max_output_tokens": 64,
		}
		resp, err := postJSON(ctx, cfg, base+"/responses", body, hdr)
		if err != nil {
			return fail("JSON Mode", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("JSON Mode", start, resp)
		}
		text, err := decodeResponseText(resp.Body)
		if err != nil {
			return fail("JSON Mode", start, err)
		}
		if text == "" {
			return failMsg("JSON Mode", start, "empty output text")
		}
		if !looksLikeJSON(text) {
			return failMsg("JSON Mode", start, "response is not valid JSON: "+text)
		}
		return pass("JSON Mode", start, text)
	}

	return skip(feature)
}

// decodeResponseText walks the Responses API output array for message text content.
func decodeResponseText(rd io.Reader) (string, error) {
	var v struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
				Type string `json:"type"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.NewDecoder(rd).Decode(&v); err != nil {
		return "", err
	}
	if v.OutputText != "" {
		return v.OutputText, nil
	}
	for _, o := range v.Output {
		for _, c := range o.Content {
			if c.Text != "" {
				return c.Text, nil
			}
		}
	}
	return "", nil
}

func init() { register(OpenAIResponseChecker{}) }
