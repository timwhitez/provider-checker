package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// GeminiChecker tests the Google Gemini API (generateContent / streamGenerateContent).
type GeminiChecker struct{}

func (GeminiChecker) Type() string { return "gemini" }

func (GeminiChecker) Supported() []string {
	return []string{"basic", "stream", "vision", "tools", "json", "system"}
}

func (c GeminiChecker) Test(ctx context.Context, cfg Config, feature, prompt string) FeatureResult {
	if prompt == "" {
		prompt = "Say hello in one short sentence."
	}
	start := time.Now()
	base := normalizeGemini(cfg.BaseURL)

	switch feature {
	case "basic":
		endpoint, err := geminiURL(base, cfg.Model, "generateContent", cfg.APIKey, false)
		if err != nil {
			return fail("Basic", start, err)
		}
		body := geminiBody(prompt, "", nil, "")
		resp, err := postJSON(ctx, cfg, endpoint, body, nil)
		if err != nil {
			return fail("Basic", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Basic", start, resp)
		}
		text, err := decodeGeminiText(resp.Body)
		if err != nil {
			return fail("Basic", start, err)
		}
		if text == "" {
			return failResponseMsg("Basic", start, "empty content", resp)
		}
		return passResponse("Basic", start, text, resp)

	case "stream":
		endpoint, err := geminiURL(base, cfg.Model, "streamGenerateContent", cfg.APIKey, true)
		if err != nil {
			return fail("Streaming", start, err)
		}
		body := geminiBody(prompt, "", nil, "")
		resp, err := postJSON(ctx, cfg, endpoint, body, nil)
		if err != nil {
			return fail("Streaming", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Streaming", start, resp)
		}
		return streamingResult(start, resp.Body, validGeminiStreamEvent)

	case "vision":
		endpoint, err := geminiURL(base, cfg.Model, "generateContent", cfg.APIKey, false)
		if err != nil {
			return fail("Vision", start, err)
		}
		// Inline base64 avoids remote fetch restrictions on Gemini.
		body := map[string]any{
			"contents": []map[string]any{
				{"role": "user", "parts": []map[string]any{
					{"text": "What color is this image? Reply with one word."},
					{"inline_data": map[string]any{
						"mime_type": "image/png",
						"data":      redPNGBase64,
					}},
				}},
			},
		}
		resp, err := postJSON(ctx, cfg, endpoint, body, nil)
		if err != nil {
			return fail("Vision", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Vision", start, resp)
		}
		text, err := decodeGeminiText(resp.Body)
		if err != nil {
			return fail("Vision", start, err)
		}
		if text == "" {
			return failResponseMsg("Vision", start, "empty content", resp)
		}
		return passResponse("Vision", start, text, resp)

	case "tools":
		endpoint, err := geminiURL(base, cfg.Model, "generateContent", cfg.APIKey, false)
		if err != nil {
			return fail("Function Calling", start, err)
		}
		body := map[string]any{
			"contents": []map[string]any{
				{"role": "user", "parts": []map[string]string{{"text": "What is the weather in Tokyo? Use the provided function."}}},
			},
			"tools": []map[string]any{
				{"function_declarations": []map[string]any{
					{"name": "get_weather", "description": "Get current weather for a city",
						"parameters": map[string]any{
							"type":       "object",
							"properties": map[string]any{"city": map[string]string{"type": "string"}},
							"required":   []string{"city"},
						}},
				}},
			},
		}
		resp, err := postJSON(ctx, cfg, endpoint, body, nil)
		if err != nil {
			return fail("Function Calling", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Function Calling", start, resp)
		}
		var r struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						FunctionCall struct {
							Name string `json:"name"`
							Args any    `json:"args"`
						} `json:"functionCall"`
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return fail("Function Calling", start, err)
		}
		for _, cand := range r.Candidates {
			for _, p := range cand.Content.Parts {
				if p.FunctionCall.Name != "" {
					ab, _ := json.Marshal(p.FunctionCall.Args)
					return passResponse("Function Calling", start, fmt.Sprintf("%s(%s)", p.FunctionCall.Name, string(ab)), resp)
				}
			}
		}
		return failResponseMsg("Function Calling", start, "no functionCall in response", resp)

	case "json":
		endpoint, err := geminiURL(base, cfg.Model, "generateContent", cfg.APIKey, false)
		if err != nil {
			return fail("JSON Mode", start, err)
		}
		body := map[string]any{
			"contents": []map[string]any{
				{"role": "user", "parts": []map[string]string{{"text": "Return a JSON object with a key 'ok' set to true."}}},
			},
			"generationConfig": map[string]any{
				"responseMimeType": "application/json",
			},
		}
		resp, err := postJSON(ctx, cfg, endpoint, body, nil)
		if err != nil {
			return fail("JSON Mode", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("JSON Mode", start, resp)
		}
		text, err := decodeGeminiText(resp.Body)
		if err != nil {
			return fail("JSON Mode", start, err)
		}
		if text == "" {
			return failResponseMsg("JSON Mode", start, "empty content", resp)
		}
		if !looksLikeJSON(text) {
			return failResponseMsg("JSON Mode", start, "response is not valid JSON: "+text, resp)
		}
		return passResponse("JSON Mode", start, text, resp)

	case "system":
		endpoint, err := geminiURL(base, cfg.Model, "generateContent", cfg.APIKey, false)
		if err != nil {
			return fail("System Instruction", start, err)
		}
		body := geminiBody("What is your codeword?", "You must reply with exactly the word PINEAPPLE and nothing else.", nil, "")
		resp, err := postJSON(ctx, cfg, endpoint, body, nil)
		if err != nil {
			return fail("System Instruction", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("System Instruction", start, resp)
		}
		text, err := decodeGeminiText(resp.Body)
		if err != nil {
			return fail("System Instruction", start, err)
		}
		if text == "" {
			return failResponseMsg("System Instruction", start, "empty content", resp)
		}
		return passResponse("System Instruction", start, text, resp)
	}

	return skip(feature)
}

func normalizeGemini(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	if !strings.Contains(base, "/v1beta") && !strings.Contains(base, "/v1") {
		base = base + "/v1beta"
	}
	return base
}

// geminiURL builds a generateContent/streamGenerateContent URL with encoded model/key.
func geminiURL(base, model, method, apiKey string, sse bool) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("model is required")
	}
	// Accept both "gemini-1.5-flash" and "models/gemini-1.5-flash".
	modelPath := strings.TrimPrefix(model, "models/")
	u, err := url.Parse(base + "/models/" + url.PathEscape(modelPath) + ":" + method)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if apiKey != "" {
		q.Set("key", apiKey)
	}
	if sse {
		q.Set("alt", "sse")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func geminiBody(prompt, system string, tools any, jsonMode string) map[string]any {
	body := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]string{{"text": prompt}}},
		},
	}
	if system != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]string{{"text": system}},
		}
	}
	if tools != nil {
		body["tools"] = tools
	}
	if jsonMode != "" {
		body["generationConfig"] = map[string]any{"responseMimeType": jsonMode}
	}
	return body
}

func decodeGeminiText(rd io.Reader) (string, error) {
	var v struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(rd).Decode(&v); err != nil {
		return "", err
	}
	for _, c := range v.Candidates {
		for _, p := range c.Content.Parts {
			if p.Text != "" {
				return p.Text, nil
			}
		}
	}
	return "", nil
}

func init() { register(GeminiChecker{}) }

func validGeminiStreamEvent(event string, value any) bool {
	if event != "" && event != "message" {
		return false
	}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if validGeminiStreamEvent("", item) {
				return true
			}
		}
		return false
	}
	candidates, _ := streamObject(value)["candidates"].([]any)
	for _, candidate := range candidates {
		object := streamObject(candidate)
		if streamObject(object["content"]) != nil {
			return true
		}
		if reason, ok := object["finishReason"].(string); ok && reason != "" {
			return true
		}
	}
	return false
}
