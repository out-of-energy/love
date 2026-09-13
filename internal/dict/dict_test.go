package dict

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

// validEntry is what the model is expected to return, escaped for embedding
// inside a chat-completions envelope.
const validEntry = `{\"word\":\"book\",\"ipa\":\"/bʊk/\",\"eli5\":\"Thing with pages.\",\"chinese\":\"书\"}`

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient("test-key", srv.URL, llm.DefaultModel, 5*time.Second)
}

func TestGenerateReturnsTheAnchorLayer(t *testing.T) {
	var gotBody string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		io.WriteString(w, `{"choices":[{"message":{"content":"`+validEntry+`"}}]}`)
	})

	entry, err := c.Generate(context.Background(), "book")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if entry.Word != "book" || entry.IPA != "/bʊk/" || entry.ELI5 != "Thing with pages." || entry.Chinese != "书" {
		t.Errorf("unexpected entry: %+v", entry)
	}
	// The anchor must be asked for in child-simple language; that register is
	// the whole reason the anchor is a separate layer from the expansion.
	if !strings.Contains(gotBody, "three-year-old") {
		t.Error("the prompt should ask for a child-simple explanation")
	}
}

func TestGeneratePrefersTheCallersSpelling(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		// The model answers with a different case; the caller's form must win.
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"word\":\"BOOK\",\"ipa\":\"/bʊk/\",\"eli5\":\"Thing with pages.\",\"chinese\":\"书\"}"}}]}`)
	})
	// The caller supplies the canonical key, and this package must not silently
	// rewrite it: normalization belongs to whoever owns the word file.
	entry, err := c.Generate(context.Background(), "ice cream")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Word != "ice cream" {
		t.Errorf("word = %q, want the caller's key echoed back", entry.Word)
	}
}

func TestGenerateRejectsIncompleteEntries(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"word\":\"book\",\"ipa\":\"/bʊk/\",\"eli5\":\"Thing with pages.\",\"chinese\":\"\"}"}}]}`)
	})

	_, err := c.Generate(context.Background(), "book")
	if !errors.Is(err, llm.ErrBadResponse) {
		t.Fatalf("want ErrBadResponse, got %v", err)
	}
}

func TestGenerateErrorsAreClassifiableByCallers(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Authentication Fails","code":"invalid_api_key"}}`)
	})

	_, err := c.Generate(context.Background(), "book")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("callers must be able to recognise an API error, got %v", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
}

func TestEntryValidate(t *testing.T) {
	full := Entry{Word: "book", IPA: "/bʊk/", ELI5: "Thing with pages.", Chinese: "书"}
	if err := full.Validate(); err != nil {
		t.Errorf("a complete entry should validate: %v", err)
	}
	for name, entry := range map[string]Entry{
		"no word":    {IPA: "/b/", ELI5: "x", Chinese: "书"},
		"no ipa":     {Word: "book", ELI5: "x", Chinese: "书"},
		"no eli5":    {Word: "book", IPA: "/b/", Chinese: "书"},
		"no chinese": {Word: "book", IPA: "/b/", ELI5: "x"},
	} {
		if err := entry.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}
