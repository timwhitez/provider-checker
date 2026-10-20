package checker

import (
	"context"
	"fmt"
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
		env, derr := decodeResponsesSyncBody(resp.Body)
		if derr != nil {
			return failResponseMsg("Basic", start, "invalid Responses payload: "+derr.Error(), resp)
		}
		evidence := responsesEvidence{ok: env.text() != "", preview: env.text(), failMsg: "empty output text"}
		return responsesProbeOutcome("Basic", start, resp, env, evidence, 64, cfg)

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
		return streamingResult(start, resp.Body, validResponsesStreamEvent)

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
		env, derr := decodeResponsesSyncBody(resp.Body)
		if derr != nil {
			return failResponseMsg("Vision", start, "invalid Responses payload: "+derr.Error(), resp)
		}
		evidence := responsesEvidence{ok: env.text() != "", preview: env.text(), failMsg: "empty output text"}
		return responsesProbeOutcome("Vision", start, resp, env, evidence, 16, cfg)

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
		env, derr := decodeResponsesSyncBody(resp.Body)
		if derr != nil {
			return failResponseMsg("Tool Calling", start, "invalid Responses payload: "+derr.Error(), resp)
		}
		// Only the first function_call item is selected and verified
		// (tool-call-local-minimum/v1).
		evidence := responsesEvidence{failMsg: "no function_call in output " + toolCallContractNote}
		if call := env.toolCall(); call != nil {
			if msg := toolCallAssertion(call.Name, call.Arguments); msg != "" {
				evidence.failMsg = msg
			} else {
				evidence.ok = true
				evidence.preview = fmt.Sprintf("%s(%s) %s", call.Name, call.Arguments, toolCallContractNote)
			}
		}
		return responsesProbeOutcome("Tool Calling", start, resp, env, evidence, 128, cfg)

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
		env, derr := decodeResponsesSyncBody(resp.Body)
		if derr != nil {
			return failResponseMsg("JSON Mode", start, "invalid Responses payload: "+derr.Error(), resp)
		}
		text := env.text()
		evidence := responsesEvidence{failMsg: "empty output text"}
		if text != "" {
			if msg := okResultAssertion(text); msg != "" {
				evidence.failMsg = msg
			} else {
				evidence.ok = true
				evidence.preview = text
			}
		}
		return responsesProbeOutcome("JSON Mode", start, resp, env, evidence, 64, cfg)
	}

	return skip(feature)
}

func init() { register(OpenAIResponseChecker{}) }

func validResponsesStreamEvent(event string, value any) bool {
	object := streamObject(value)
	switch streamEventType(event, object) {
	case "response.created", "response.in_progress", "response.completed", "response.done", "response.incomplete":
		return streamObject(object["response"]) != nil
	case "response.output_text.delta", "response.refusal.delta", "response.function_call_arguments.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		_, ok := object["delta"].(string)
		return ok
	case "response.output_text.done", "response.reasoning_summary_text.done", "response.reasoning_text.done":
		_, ok := object["text"].(string)
		return ok
	case "response.refusal.done":
		_, ok := object["refusal"].(string)
		return ok
	case "response.function_call_arguments.done":
		_, ok := object["arguments"].(string)
		return ok
	case "response.output_item.added", "response.output_item.done":
		return streamObject(object["item"]) != nil
	case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return streamObject(object["part"]) != nil
	case "response.output_text.annotation.added":
		return streamObject(object["annotation"]) != nil
	}
	return false
}
