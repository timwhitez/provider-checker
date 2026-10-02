package checker

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

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

const chatStreamFixture = "data: {\"model\":\"concrete\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"

type splitStreamReader struct {
	data     []byte
	sizes    []int
	reads    int
	finalErr error
}

func (r *splitStreamReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		if r.finalErr != nil {
			return 0, r.finalErr
		}
		return 0, io.EOF
	}
	n := len(p)
	if len(r.sizes) > 0 && r.sizes[r.reads%len(r.sizes)] < n {
		n = r.sizes[r.reads%len(r.sizes)]
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	r.reads++
	if len(r.data) == 0 && r.finalErr != nil {
		return n, r.finalErr
	}
	return n, nil
}

func TestStreamFramingIndependentOfReadBoundaries(t *testing.T) {
	// Multiline JSON, comments, ignored extension fields, and empty data frames.
	input := ": keepalive\nunknown: value\ndata:\n\n" +
		"event: response.created\nid: 123\ndata: {\"type\":\"response.created\",\ndata: \"response\":{\"model\":\"alias\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"terminal\"}}\n\n" +
		"data: [DONE]\n\n"
	rng := rand.New(rand.NewSource(1))
	sizes := make([]int, 20)
	for i := range sizes {
		sizes[i] = 1 + rng.Intn(50)
	}
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		for _, chunks := range [][]int{{1}, sizes, {4096}} {
			body := strings.ReplaceAll(input, "\n", ending)
			got, err := readStream(&splitStreamReader{data: []byte(body), sizes: chunks}, validResponsesStreamEvent)
			if err != nil {
				t.Fatal(err)
			}
			if got.Events != 2 || got.Model != "terminal" || got.Partial {
				t.Fatalf("ending=%q chunks=%v: %+v", ending, chunks, got)
			}
		}
	}
}

func TestStreamPartialPolicyAndErrorPrecedence(t *testing.T) {
	broken := errors.New("connection reset by peer")
	for _, tc := range []struct {
		name, body string
		readErr    error
		events     int
		partial    bool
		wantErr    string
	}{
		{"clean EOF without DONE", chatStreamFixture, nil, 1, false, ""},
		{"valid data and read error together", chatStreamFixture, broken, 1, true, "connection reset"},
		{"only partial data and read error", "data: {\"choices\":[{\"delta\":{}}]}", broken, 0, false, "connection reset"},
		{"only unclosed frame", strings.TrimSuffix(chatStreamFixture, "\n"), nil, 0, false, ""},
		{"valid then unclosed frame", chatStreamFixture + "data: {", nil, 1, true, ""},
		{"valid then error event", chatStreamFixture + "event: error\ndata: {}\n\n", nil, 1, false, "error event"},
		{"valid then error payload", chatStreamFixture + "data: {\"error\":{\"message\":\"failed\"}}\n\n", nil, 1, false, "error/failed"},
		{"valid then failed type", chatStreamFixture + "data: {\"type\":\"response.failed\"}\n\n", nil, 1, false, "error/failed"},
		{"valid then invalid JSON", chatStreamFixture + "data: {\n\n", broken, 1, false, "invalid JSON"},
		{"empty", "", nil, 0, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readStream(&splitStreamReader{data: []byte(tc.body), finalErr: tc.readErr}, validChatStreamEvent)
			if got.Events != tc.events || got.Partial != tc.partial {
				t.Fatalf("result=%+v, want events=%d partial=%v", got, tc.events, tc.partial)
			}
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error=%v, want substring %q", err, tc.wantErr)
			}
			if tc.partial && got.PartialReason == "" {
				t.Fatal("partial reason missing")
			}
			if tc.readErr != nil && tc.wantErr == "connection reset" && !errors.Is(err, broken) {
				t.Fatal("transport error lost")
			}
		})
	}
}

func TestStreamBudgetsAndUnicodeSnippet(t *testing.T) {
	for _, size := range []int{streamFrameLimit, streamFrameLimit + 1} {
		// Include physical delimiters in the frame budget.
		body := ":" + strings.Repeat("x", size-len(chatStreamFixture)-2) + "\n" + chatStreamFixture
		got, err := readStream(strings.NewReader(body), validChatStreamEvent)
		if size == streamFrameLimit && (err != nil || got.Events != 1) {
			t.Fatalf("boundary: %+v %v", got, err)
		}
		if size > streamFrameLimit && (err == nil || !strings.Contains(err.Error(), "frame exceeds")) {
			t.Fatalf("overflow: %v", err)
		}
	}
	for _, size := range []int{streamReadLimit, streamReadLimit + 1} {
		body := chatStreamFixture + strings.Repeat("\n", size-len(chatStreamFixture))
		got, err := readStream(strings.NewReader(body), validChatStreamEvent)
		if size == streamReadLimit && (err != nil || got.Events != 1) {
			t.Fatalf("total boundary: %+v %v", got, err)
		}
		if size > streamReadLimit && (err == nil || got.Partial || !strings.Contains(err.Error(), "stream exceeds")) {
			t.Fatalf("total overflow: %+v %v", got, err)
		}
	}
	body := chatStreamFixture + "data: " + strings.Repeat("x", streamFrameLimit) + "\n\n"
	got, err := readStream(strings.NewReader(body), validChatStreamEvent)
	if got.Events != 1 || err == nil || got.Partial {
		t.Fatalf("overflow after progress: %+v %v", got, err)
	}
	body = "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("好", 140) + "\"}}]}\n\n"
	got, err = readStream(strings.NewReader(body), validChatStreamEvent)
	if err != nil || !utf8.ValidString(got.Snippet) || utf8.RuneCountInString(got.Snippet) != streamSnippetLimit {
		t.Fatalf("unicode snippet: %q %v", got.Snippet, err)
	}
}

func TestStreamingResultMakesPartialAndFailureVisible(t *testing.T) {
	broken := errors.New("connection reset by peer")
	got := streamingResult(time.Now(), &splitStreamReader{data: []byte(chatStreamFixture), finalErr: broken}, validChatStreamEvent)
	if got.Status != StatusPass || !strings.Contains(got.Detail, "1 events") || !strings.Contains(got.Detail, "partial: connection reset") {
		t.Fatalf("partial result: %+v", got)
	}
	got = streamingResult(time.Now(), strings.NewReader(chatStreamFixture+"data: {\n\n"), validChatStreamEvent)
	if got.Status != StatusFail || !strings.Contains(got.Error, "invalid JSON") || got.UpstreamResponseModel != "concrete" {
		t.Fatalf("protocol failure: %+v", got)
	}
	got = streamingResult(time.Now(), strings.NewReader(chatStreamFixture+"data: {"), validChatStreamEvent)
	if got.Status != StatusPass || !strings.Contains(got.Detail, "partial: truncated SSE frame") {
		t.Fatalf("truncated result: %+v", got)
	}
}

func TestStreamFailureRetainsTerminalModel(t *testing.T) {
	body := "data: {\"type\":\"response.created\",\"response\":{\"model\":\"alias\"}}\n\n" +
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"model\":\"terminal\",\"status\":\"failed\"}}\n\n"
	result := streamingResult(time.Now(), strings.NewReader(body), validResponsesStreamEvent)
	if result.Status != StatusFail || result.UpstreamResponseModel != "terminal" {
		t.Fatalf("result=%+v", result)
	}
}

func TestStreamUnknownEventsAndModelsDoNotProveProgress(t *testing.T) {
	body := "data: {\"model\":\"untrusted-proof\"}\n\n" + chatStreamFixture +
		"data: {\"model\":\"later-nonterminal\",\"choices\":[{\"delta\":{}}]}\n\n"
	got, err := readStream(strings.NewReader(body), validChatStreamEvent)
	if err != nil || got.Events != 2 || got.Model != "concrete" {
		t.Fatalf("result=%+v error=%v", got, err)
	}
	for _, body := range []string{
		"event: response.created\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n",
		"event: response.unknown\ndata: {\"response\":{\"model\":\"unknown\"}}\n\n",
	} {
		got, err := readStream(strings.NewReader(body), validResponsesStreamEvent)
		if err != nil || got.Events != 0 || got.Model != "" {
			t.Fatalf("unknown typed event: %+v %v", got, err)
		}
	}
}

func TestStreamLeadingBOM(t *testing.T) {
	for _, chunks := range [][]int{{1}, {2}, {4096}} {
		for _, tc := range []struct {
			name, body string
			wantEvents int
			wantError  bool
		}{
			{"data first", "\ufeff" + chatStreamFixture, 1, false},
			{"error event first", "\ufeffevent: error\ndata: {}\n\n" + chatStreamFixture, 0, true},
		} {
			t.Run(fmt.Sprintf("%s/chunks=%v", tc.name, chunks), func(t *testing.T) {
				got, err := readStream(&splitStreamReader{data: []byte(tc.body), sizes: chunks}, validChatStreamEvent)
				if got.Events != tc.wantEvents || (err != nil) != tc.wantError {
					t.Fatalf("result=%+v error=%v; want events=%d error=%v", got, err, tc.wantEvents, tc.wantError)
				}
			})
		}
	}
}

func TestStreamBOMOnlyAtStartAndBudgeted(t *testing.T) {
	for _, body := range []string{
		"\ufeff\ufeff" + chatStreamFixture,               // Strip exactly one.
		"\n\ufeff" + chatStreamFixture,                   // A BOM after even an empty line is data.
		chatStreamFixture + "\ufeff" + chatStreamFixture, // Later frame field untouched.
	} {
		got, err := readStream(&splitStreamReader{data: []byte(body), sizes: []int{1}}, validChatStreamEvent)
		want := 0
		if strings.HasPrefix(body, chatStreamFixture) {
			want = 1
		}
		if err != nil || got.Events != want {
			t.Fatalf("body=%q result=%+v error=%v", body, got, err)
		}
	}
	body := "\ufeffdata: {\"choices\":[{\"delta\":{\"content\":\"a\ufeffb\"}}]}\n\n"
	got, err := readStream(&splitStreamReader{data: []byte(body), sizes: []int{1}}, validChatStreamEvent)
	if err != nil || got.Events != 1 || !strings.Contains(got.Snippet, "a\ufeffb") {
		t.Fatalf("body BOM lost: %+v %v", got, err)
	}
	for _, size := range []int{streamFrameLimit, streamFrameLimit + 1} {
		body = "\ufeff:" + strings.Repeat("x", size-len(chatStreamFixture)-5) + "\n" + chatStreamFixture
		got, err = readStream(strings.NewReader(body), validChatStreamEvent)
		if size == streamFrameLimit && (err != nil || got.Events != 1) {
			t.Fatalf("BOM frame boundary: %+v %v", got, err)
		}
		if size > streamFrameLimit && (err == nil || !strings.Contains(err.Error(), "frame exceeds")) {
			t.Fatalf("BOM frame overflow: %v", err)
		}
	}
	for _, size := range []int{streamReadLimit, streamReadLimit + 1} {
		body = "\ufeff" + chatStreamFixture + strings.Repeat("\n", size-len(chatStreamFixture)-3)
		got, err = readStream(strings.NewReader(body), validChatStreamEvent)
		if size == streamReadLimit && (err != nil || got.Events != 1) {
			t.Fatalf("BOM total boundary: %+v %v", got, err)
		}
		if size > streamReadLimit && (err == nil || !strings.Contains(err.Error(), "stream exceeds")) {
			t.Fatalf("BOM total overflow: %v", err)
		}
	}
}
