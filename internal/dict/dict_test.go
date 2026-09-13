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
)

// validRecord is the JSON body the model is expected to return, escaped for
// embedding inside a chat-completions envelope.
const validRecord = `{\"word\":\"book\",\"ipa\":\"/bʊk/\",\"eli5\":\"Thing with pages.\",\"chinese\":\"书\"}`

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient("test-key", srv.URL, DefaultModel, 5*time.Second)
}

func TestGenerateParsesTheRecord(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		io.WriteString(w, `{"choices":[{"message":{"content":"`+validRecord+`"}}]}`)
	})

	rec, err := c.Generate(context.Background(), "book")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if rec.Word != "book" || rec.IPA != "/bʊk/" || rec.Chinese != "书" {
		t.Errorf("unexpected record: %+v", rec)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if !strings.HasSuffix(gotPath, "/chat/completions") {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{`"response_format":{"type":"json_object"}`, `"thinking":{"type":"disabled"}`, `"stream":false`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("request body missing %s: %s", want, gotBody)
		}
	}
	if strings.Contains(gotBody, "test-key") {
		t.Error("the API key must never appear in the request body")
	}
}

func TestGenerateStripsMarkdownFences(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"Here you go:\n`+"```"+`json\n`+validRecord+`\n`+"```"+`"}}]}`)
	})
	rec, err := c.Generate(context.Background(), "book")
	if err != nil {
		t.Fatalf("fenced JSON should still parse: %v", err)
	}
	if rec.Chinese != "书" {
		t.Errorf("unexpected record: %+v", rec)
	}
}

func TestGeneratePrefersTheCallersSpelling(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		// The model answers with a different case; the caller's form must win.
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"word\":\"BOOK\",\"ipa\":\"/bʊk/\",\"eli5\":\"Thing with pages.\",\"chinese\":\"书\"}"}}]}`)
	})
	// The caller supplies the canonical key, and this package must not silently
	// rewrite it: normalization belongs to whoever owns the word file.
	rec, err := c.Generate(context.Background(), "ice cream")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Word != "ice cream" {
		t.Errorf("word = %q, want the caller's key echoed back", rec.Word)
	}
}

func TestGenerateReportsAPIError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Authentication Fails","code":"invalid_api_key"}}`)
	})

	_, err := c.Generate(context.Background(), "book")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "invalid_api_key" {
		t.Errorf("unexpected APIError: %+v", apiErr)
	}
	if apiErr.Retryable() {
		t.Error("a 401 must not be retryable")
	}
	if !strings.Contains(apiErr.Error(), "Authentication Fails") {
		t.Errorf("error text should carry the API message: %q", apiErr.Error())
	}
}

func TestGenerateDoesNotRetryPermanentErrors(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"nope","code":"invalid_api_key"}}`)
	})
	if _, err := c.Generate(context.Background(), "book"); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("made %d calls, want exactly 1 for a permanent error", calls)
	}
}

func TestGenerateFallsBackWhenThinkingIsRejected(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"thinking"`) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"unknown field thinking","code":"invalid_request_error"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"`+validRecord+`"}}]}`)
	})

	rec, err := c.Generate(context.Background(), "book")
	if err != nil {
		t.Fatalf("the compatible fallback should have succeeded: %v", err)
	}
	if rec.Chinese != "书" {
		t.Errorf("unexpected record: %+v", rec)
	}
	if calls != 2 {
		t.Errorf("made %d calls, want 2 (thinking, then plain)", calls)
	}
}

func TestGenerateRetriesServerErrorsOnce(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"boom","code":"server_error"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"`+validRecord+`"}}]}`)
	})

	if _, err := c.Generate(context.Background(), "book"); err != nil {
		t.Fatalf("a 5xx should be retried once: %v", err)
	}
	if calls != 2 {
		t.Errorf("made %d calls, want 2", calls)
	}
}

func TestGenerateRejectsIncompleteRecords(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"word\":\"book\",\"ipa\":\"/bʊk/\",\"eli5\":\"Thing with pages.\",\"chinese\":\"\"}"}}]}`)
	})
	_, err := c.Generate(context.Background(), "book")
	if !errors.Is(err, ErrBadResponse) {
		t.Fatalf("want ErrBadResponse, got %v", err)
	}
}

func TestGenerateReportsEmptyChoices(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	})
	if _, err := c.Generate(context.Background(), "book"); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("want ErrBadResponse, got %v", err)
	}
}

func TestContextCancellationIsNotRetried(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"choices":[{"message":{"content":"`+validRecord+`"}}]}`)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Generate(ctx, "book"); err == nil {
		t.Fatal("expected a cancellation error")
	}
	if calls != 0 {
		t.Errorf("made %d calls, want 0 for an already-cancelled context", calls)
	}
}
