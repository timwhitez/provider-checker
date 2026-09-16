package checker

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Config holds the user-supplied connection settings for a provider test.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration

	// ReasoningEffort maps to the OpenAI-style `reasoning.effort` /
	// `reasoning_effort` parameter. Allowed values: "", "none", "low",
	// "medium", "high", "xhigh", "max". Empty means "do not send the parameter".
	ReasoningEffort string
	// ReasoningMode maps to the Responses API `reasoning.mode` field.
	// Allowed values: "", "pro". Empty means "do not send the parameter".
	ReasoningMode string
}

// ReasoningEfforts is the ordered set of selectable reasoning-effort values.
// The empty first entry means "omit the field / provider default (medium)".
// Values match the OpenAI reasoning guide: none, low, medium, high,
// xhigh, max (support is model-dependent).
var ReasoningEfforts = []string{"", "none", "low", "medium", "high", "xhigh", "max"}

// ReasoningModes is the ordered set of selectable reasoning-mode values.
// The empty first entry means "omit the field / standard (default)".
var ReasoningModes = []string{"", "pro"}

// Status of a single feature test.
type Status int

const (
	StatusSkip Status = iota
	StatusPass
	StatusFail
)

func (s Status) String() string {
	switch s {
	case StatusPass:
		return "PASS"
	case StatusFail:
		return "FAIL"
	default:
		return "SKIP"
	}
}

// FeatureResult is the outcome of testing one feature.
type FeatureResult struct {
	Name                  string
	Status                Status
	Latency               time.Duration
	UpstreamResponseModel string
	Detail                string
	Error                 string
}

// Feature describes a testable capability.
type Feature struct {
	Key  string
	Name string
}

// UnifiedFeatures is the fixed capability set shown as checkboxes in the UI.
// Every checker maps these keys to its own concrete request shape.
var UnifiedFeatures = []Feature{
	{Key: "basic", Name: "基础对话 / Basic"},
	{Key: "stream", Name: "流式输出 / Streaming"},
	{Key: "vision", Name: "视觉理解 / Vision"},
	{Key: "tools", Name: "工具调用 / Tool Calling"},
	{Key: "json", Name: "JSON 模式 / JSON Mode"},
	{Key: "max_tokens", Name: "Token 上限 / Max Tokens"},
	{Key: "system", Name: "系统提示 / System Prompt"},
}

// FeatureName returns the display name for a unified key.
func FeatureName(key string) string {
	for _, f := range UnifiedFeatures {
		if f.Key == key {
			return f.Name
		}
	}
	return key
}

// Checker is implemented by each provider type.
type Checker interface {
	Type() string
	// Supported returns the subset of UnifiedFeatures keys this provider handles.
	Supported() []string
	// Test runs one unified feature key and returns the result.
	Test(ctx context.Context, cfg Config, feature string, prompt string) FeatureResult
}

// AllCheckers maps provider type key -> checker.
var AllCheckers = map[string]Checker{}

func register(c Checker) {
	AllCheckers[c.Type()] = c
}

// ProviderTypes returns the ordered list of selectable provider types.
func ProviderTypes() []string {
	return []string{
		OpenAIChatChecker{}.Type(),
		OpenAIResponseChecker{}.Type(),
		AnthropicChecker{}.Type(),
		GeminiChecker{}.Type(),
	}
}

// ProviderLabel is the friendly dropdown label for a provider type.
func ProviderLabel(t string) string {
	switch t {
	case "openai-chat":
		return "OpenAI Chat Completions"
	case "openai-response":
		return "OpenAI Responses API"
	case "anthropic":
		return "Anthropic Messages"
	case "gemini":
		return "Google Gemini"
	}
	return t
}

// Supports reports whether key is in the checker's supported set.
func Supports(c Checker, key string) bool {
	for _, k := range c.Supported() {
		if k == key {
			return true
		}
	}
	return false
}

// Shared HTTP client with timeout.
func httpClient(cfg Config) *http.Client {
	t := cfg.Timeout
	if t <= 0 {
		t = 60 * time.Second
	}
	return &http.Client{Timeout: t}
}

// newReq creates an HTTP request bound to ctx (shared by all checkers).
func newReq(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, url, body)
}

// applyChatReasoning adds the Chat Completions `reasoning_effort` field when set.
func applyChatReasoning(body map[string]any, cfg Config) {
	if cfg.ReasoningEffort != "" {
		body["reasoning_effort"] = cfg.ReasoningEffort
	}
}

// applyResponsesReasoning adds the Responses API `reasoning` object (effort/mode)
// when either value is set.
func applyResponsesReasoning(body map[string]any, cfg Config) {
	if cfg.ReasoningEffort == "" && cfg.ReasoningMode == "" {
		return
	}
	reasoning := map[string]any{}
	if cfg.ReasoningEffort != "" {
		reasoning["effort"] = cfg.ReasoningEffort
	}
	if cfg.ReasoningMode != "" {
		reasoning["mode"] = cfg.ReasoningMode
	}
	body["reasoning"] = reasoning
}
