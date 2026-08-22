package checker

import "testing"

func TestStatusString(t *testing.T) {
	cases := []struct {
		s    Status
		want string
	}{
		{StatusPass, "PASS"},
		{StatusFail, "FAIL"},
		{StatusSkip, "SKIP"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("Status(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestProviderTypes(t *testing.T) {
	types := ProviderTypes()
	want := []string{"openai-chat", "openai-response", "anthropic", "gemini"}
	if len(types) != len(want) {
		t.Fatalf("ProviderTypes() len = %d, want %d (%v)", len(types), len(want), types)
	}
	for i, w := range want {
		if types[i] != w {
			t.Errorf("ProviderTypes()[%d] = %q, want %q", i, types[i], w)
		}
	}
}

func TestAllCheckersRegistered(t *testing.T) {
	for _, typ := range ProviderTypes() {
		c, ok := AllCheckers[typ]
		if !ok {
			t.Errorf("AllCheckers[%q] not registered", typ)
			continue
		}
		if c.Type() != typ {
			t.Errorf("checker.Type() = %q, want %q", c.Type(), typ)
		}
		if len(c.Supported()) == 0 {
			t.Errorf("checker %q has empty Supported()", typ)
		}
	}
}

func TestProviderLabel(t *testing.T) {
	cases := map[string]string{
		"openai-chat":     "OpenAI Chat Completions",
		"openai-response": "OpenAI Responses API",
		"anthropic":       "Anthropic Messages",
		"gemini":          "Google Gemini",
		"unknown":         "unknown",
	}
	for in, want := range cases {
		if got := ProviderLabel(in); got != want {
			t.Errorf("ProviderLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFeatureName(t *testing.T) {
	if got := FeatureName("basic"); got == "basic" {
		t.Errorf("FeatureName(basic) should return display name, got %q", got)
	}
	if got := FeatureName("nonexistent"); got != "nonexistent" {
		t.Errorf("FeatureName(unknown) = %q, want %q", got, "nonexistent")
	}
}

func TestSupports(t *testing.T) {
	c := AllCheckers["openai-chat"]
	if !Supports(c, "basic") {
		t.Error("openai-chat should support basic")
	}
	if Supports(c, "nonexistent-feature") {
		t.Error("openai-chat should not support nonexistent-feature")
	}
}

func TestNormalizeV1(t *testing.T) {
	cases := []struct {
		in, def, want string
	}{
		{"", "https://api.openai.com", "https://api.openai.com/v1"},
		{"https://api.openai.com", "https://api.openai.com", "https://api.openai.com/v1"},
		{"https://api.openai.com/v1", "https://api.openai.com", "https://api.openai.com/v1"},
		{"https://api.openai.com/v1/", "https://api.openai.com", "https://api.openai.com/v1"},
		{"https://proxy.example.com/openai/v1", "x", "https://proxy.example.com/openai/v1"},
	}
	for _, c := range cases {
		if got := normalizeV1(c.in, c.def); got != c.want {
			t.Errorf("normalizeV1(%q, %q) = %q, want %q", c.in, c.def, got, c.want)
		}
	}
}

func TestUnifiedFeaturesCoverSupported(t *testing.T) {
	// Every supported key declared by a checker must exist in UnifiedFeatures.
	valid := map[string]bool{}
	for _, f := range UnifiedFeatures {
		valid[f.Key] = true
	}
	for _, typ := range ProviderTypes() {
		c := AllCheckers[typ]
		for _, k := range c.Supported() {
			if !valid[k] {
				t.Errorf("checker %q declares supported key %q not in UnifiedFeatures", typ, k)
			}
		}
	}
}
