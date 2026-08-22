package checker

import "testing"

func TestLooksLikeJSON(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"ok":true}`, true},
		{`[1,2,3]`, true},
		{`  "x"  `, true},
		{`not json`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := looksLikeJSON(c.in); got != c.want {
			t.Errorf("looksLikeJSON(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGeminiURL(t *testing.T) {
	u, err := geminiURL("https://generativelanguage.googleapis.com/v1beta", "gemini-1.5-flash", "generateContent", "abc+def", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=abc%2Bdef"; u != want {
		t.Fatalf("url = %q, want %q", u, want)
	}

	u2, err := geminiURL("https://example.com/v1beta", "models/gemini-2.0", "streamGenerateContent", "k", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://example.com/v1beta/models/gemini-2.0:streamGenerateContent?alt=sse&key=k"; u2 != want {
		t.Fatalf("stream url = %q, want %q", u2, want)
	}
}

func TestNormalizeGemini(t *testing.T) {
	if got := normalizeGemini(""); got != "https://generativelanguage.googleapis.com/v1beta" {
		t.Fatalf("empty = %q", got)
	}
	if got := normalizeGemini("https://proxy.example.com"); got != "https://proxy.example.com/v1beta" {
		t.Fatalf("proxy = %q", got)
	}
	if got := normalizeGemini("https://proxy.example.com/v1beta/"); got != "https://proxy.example.com/v1beta" {
		t.Fatalf("already = %q", got)
	}
}
