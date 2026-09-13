package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/out-of-energy/love/internal/llm"
)

// goodContent is what the model is expected to return, escaped for embedding
// inside a chat-completions envelope.
const goodContent = `{\"meaning\":\"to keep something in good condition\",\"examples\":[\"I maintain my bicycle every month.\"],\"scene\":\"Someone taking care of what they own.\",\"dialogue\":[{\"speaker\":\"A\",\"line\":\"Your bike still looks new.\"},{\"speaker\":\"B\",\"line\":\"I maintain it every month.\"}]}`

// contentMissingTheWord is the failure this package exists to catch: a
// perfectly plausible reply whose dialogue never uses the target word.
const contentMissingTheWord = `{\"meaning\":\"to keep something in good condition\",\"examples\":[\"I maintain my bicycle.\"],\"scene\":\"Someone taking care of what they own.\",\"dialogue\":[{\"speaker\":\"A\",\"line\":\"Do you ride often?\"},{\"speaker\":\"B\",\"line\":\"Every weekend.\"}]}`

func testProvider(t *testing.T, handler http.HandlerFunc) *DeepSeek {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewDeepSeek("test-key", srv.URL, llm.DefaultModel, 5*time.Second)
}

func reply(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"`+body+`"}}]}`)
	}
}

func TestGenerateContentParsesTheExpansion(t *testing.T) {
	var gotBody string
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		io.WriteString(w, `{"choices":[{"message":{"content":"`+goodContent+`"}}]}`)
	})

	content, err := p.GenerateContent(context.Background(), "maintain")
	if err != nil {
		t.Fatalf("GenerateContent failed: %v", err)
	}
	if content.Meaning == "" || len(content.Examples) != 1 || len(content.Dialogue) != 2 {
		t.Errorf("unexpected content: %+v", content)
	}
	if content.Dialogue[1].Speaker != "B" || !strings.Contains(content.Dialogue[1].Line, "maintain") {
		t.Errorf("dialogue was not parsed correctly: %+v", content.Dialogue)
	}
	// The meaning must not be asked for in child-simple language: that is the
	// anchor's job, and conflating them is what made two layers look redundant.
	if !strings.Contains(gotBody, "ordinary adult English") {
		t.Error("the prompt should ask for ordinary adult phrasing")
	}
	if !strings.Contains(gotBody, "MUST contain the word") {
		t.Error("the prompt should demand the word appear in the dialogue")
	}
}

func TestGenerateContentRejectsADialogueThatNeverUsesTheWord(t *testing.T) {
	var calls int
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"choices":[{"message":{"content":"`+contentMissingTheWord+`"}}]}`)
	})

	_, err := p.GenerateContent(context.Background(), "maintain")
	if !errors.Is(err, llm.ErrBadResponse) {
		t.Fatalf("want ErrBadResponse, got %v", err)
	}
	if !strings.Contains(err.Error(), "maintain") {
		t.Errorf("the error should name the word, got %q", err.Error())
	}
	// A contract violation is worth exactly one retry, not an endless loop.
	if calls != 2 {
		t.Errorf("made %d calls, want 2", calls)
	}
}

func TestGenerateContentRecoversWhenTheRetryIsGood(t *testing.T) {
	var calls int
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, `{"choices":[{"message":{"content":"`+contentMissingTheWord+`"}}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"`+goodContent+`"}}]}`)
	})

	content, err := p.GenerateContent(context.Background(), "maintain")
	if err != nil {
		t.Fatalf("the retry should have succeeded: %v", err)
	}
	if len(content.Dialogue) != 2 {
		t.Errorf("unexpected content: %+v", content)
	}
	if calls != 2 {
		t.Errorf("made %d calls, want 2", calls)
	}
}

// A first reply that omits a field must not leave that field behind for the
// second attempt to inherit, or an incomplete answer would look complete.
func TestGenerateContentDoesNotInheritFieldsAcrossAttempts(t *testing.T) {
	var calls int
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// Parses, but the dialogue misses the word, so it is retried.
			io.WriteString(w, `{"choices":[{"message":{"content":"`+contentMissingTheWord+`"}}]}`)
			return
		}
		// The retry has no meaning at all. If the previous meaning survived,
		// this would wrongly validate.
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"examples\":[\"x\"],\"scene\":\"y\",\"dialogue\":[{\"speaker\":\"A\",\"line\":\"maintain this\"}]}"}}]}`)
	})

	_, err := p.GenerateContent(context.Background(), "maintain")
	if err == nil {
		t.Fatal("an incomplete retry must not validate")
	}
	if !errors.Is(err, llm.ErrBadResponse) {
		t.Errorf("want ErrBadResponse, got %v", err)
	}
}

func TestGenerateContentReportsAPIErrors(t *testing.T) {
	p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Authentication Fails","code":"invalid_api_key"}}`)
	})

	_, err := p.GenerateContent(context.Background(), "maintain")
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
}

func TestDeepSeekNamesItself(t *testing.T) {
	p := NewDeepSeek("k", llm.DefaultBaseURL, llm.DefaultModel, time.Second)
	if p.Name() != "deepseek" {
		t.Errorf("Name() = %q, want deepseek so cached content records its origin", p.Name())
	}
}

func TestContentValidate(t *testing.T) {
	base := Content{
		Meaning:  "to keep something working",
		Examples: []string{"I maintain my bicycle."},
		Scene:    "Someone caring for what they own.",
		Dialogue: []Line{{Speaker: "A", Line: "Your bike looks new."}, {Speaker: "B", Line: "I maintain it."}},
	}
	if err := base.Validate("maintain"); err != nil {
		t.Errorf("valid content was rejected: %v", err)
	}

	cases := map[string]func(c *Content){
		"no meaning":        func(c *Content) { c.Meaning = "  " },
		"no examples":       func(c *Content) { c.Examples = nil },
		"no dialogue":       func(c *Content) { c.Dialogue = nil },
		"empty second line": func(c *Content) { c.Dialogue[1].Line = "" },
	}
	for name, mutate := range cases {
		c := base
		c.Examples = append([]string(nil), base.Examples...)
		c.Dialogue = append([]Line(nil), base.Dialogue...)
		mutate(&c)
		if err := c.Validate("maintain"); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestUsesWordMatchesInflectionsAndIgnoresCase(t *testing.T) {
	cases := []struct {
		text, word string
		want       bool
	}{
		{"I maintain it.", "maintain", true},
		{"She MAINTAINS a garden.", "maintain", true},
		{"I maintain it.", "MAINTAIN", true},
		{"Nothing here.", "maintain", false},
		{"anything", "", false},
	}
	for _, tc := range cases {
		if got := UsesWord(tc.text, tc.word); got != tc.want {
			t.Errorf("UsesWord(%q, %q) = %v, want %v", tc.text, tc.word, got, tc.want)
		}
	}
}
