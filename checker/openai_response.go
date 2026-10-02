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
			return failResponseMsg("Basic", start, "empty output text", resp)
		}
		return passResponse("Basic", start, text, resp)

	case "stream":
		body := map[string]any{
			"model":             cfg.Model,
			"input":             prompt,
			"max_output_tokens": 32,
			"stream":            true,
		}
		applyResponsesReasoning(body, cfg)
		resp, err := postJSON(ctx, cfg, base+"/responses", body, hdr)
		if err != nil {
			return fail("Streaming", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Streaming", start, resp)
		}
		n, snippet, model, err := readStream(resp.Body)
		if err != nil {
			return fail("Streaming", start, err)
		}
		if n == 0 {
			return failMsg("Streaming", start, "no chunks received")
		}
		return passWithResponseModel("Streaming", start, fmt.Sprintf("%d chunks; %s", n, snippet), model)

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
		applyResponsesReasoning(body, cfg)
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
			return failResponseMsg("Vision", start, "empty output text", resp)
		}
		return passResponse("Vision", start, text, resp)

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
		applyResponsesReasoning(body, cfg)
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
				return passResponse("Tool Calling", start, fmt.Sprintf("%s(%s)", o.Name, o.Args), resp)
			}
		}
		return failResponseMsg("Tool Calling", start, "no function_call in output", resp)

	case "json":
		body := map[string]any{
			"model": cfg.Model,
			"input": "Return a JSON object with a key 'ok' set to true.",
			"text": map[string]any{
				"format": map[string]any{
					"type":   "json_schema",
					"name":   "ok_result",
					"strict": true,
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
		applyResponsesReasoning(body, cfg)
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
			return failResponseMsg("JSON Mode", start, err.Error(), resp)
		}
		if text == "" {
			return failResponseMsg("JSON Mode", start, "empty output text", resp)
		}
		if msg := okResultAssertion(text); msg != "" {
			return failResponseMsg("JSON Mode", start, msg, resp)
		}
		return passResponse("JSON Mode", start, text, resp)
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
	var text string
	for _, o := range v.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			if c.Type == "output_text" {
				text += c.Text
			}
		}
	}
	return text, nil
}

func init() { register(OpenAIResponseChecker{}) }
