package llm

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

// payload is a stand-in for whatever a caller is decoding into.
type payload struct {
	Word string `json:"word"`
	IPA  string `json:"ipa"`
}

const goodJSON = `{\"word\":\"book\",\"ipa\":\"/bʊk/\"}`

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient("test-key", srv.URL, DefaultModel, 5*time.Second)
}

func reply(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"`+content+`"}}]}`)
	}
}

func TestCompleteSendsAuthAndTheOutputContract(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		io.WriteString(w, `{"choices":[{"message":{"content":"`+goodJSON+`"}}]}`)
	})

	var out payload
	if err := c.Complete(context.Background(), Request{System: "sys", User: "book", Out: &out}); err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if out.Word != "book" || out.IPA != "/bʊk/" {
		t.Errorf("decoded %+v", out)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if !strings.HasSuffix(gotPath, "/chat/completions") {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{
		`"response_format":{"type":"json_object"}`,
		`"thinking":{"type":"disabled"}`,
		`"stream":false`,
		`"role":"system"`,
		`"role":"user"`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("request body missing %s: %s", want, gotBody)
		}
	}
	if strings.Contains(gotBody, "test-key") {
		t.Error("the API key must never appear in the request body")
	}
}

func TestCompleteStripsMarkdownFences(t *testing.T) {
	// The newlines must reach the API as JSON escapes, not as raw newlines.
	c := testClient(t, reply("Here you go:\\n```json\\n"+goodJSON+"\\n```"))

	var out payload
	if err := c.Complete(context.Background(), Request{Out: &out}); err != nil {
		t.Fatalf("fenced JSON should still parse: %v", err)
	}
	if out.Word != "book" {
		t.Errorf("decoded %+v", out)
	}
}

func TestCompleteRequiresAJSONObject(t *testing.T) {
	c := testClient(t, reply("I am afraid I cannot help with that."))

	var out payload
	err := c.Complete(context.Background(), Request{Out: &out})
	if !errors.Is(err, ErrBadResponse) {
		t.Fatalf("want ErrBadResponse, got %v", err)
	}
}

func TestCompleteReportsAPIError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Authentication Fails","code":"invalid_api_key"}}`)
	})

	var out payload
	err := c.Complete(context.Background(), Request{Out: &out})
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

func TestCompleteDoesNotRetryPermanentErrors(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"nope","code":"invalid_api_key"}}`)
	})

	var out payload
	if err := c.Complete(context.Background(), Request{Out: &out}); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("made %d calls, want exactly 1 for a permanent error", calls)
	}
}

func TestCompleteFallsBackWhenThinkingIsRejected(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"thinking"`) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"unknown field thinking","code":"invalid_request_error"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"`+goodJSON+`"}}]}`)
	})

	var out payload
	if err := c.Complete(context.Background(), Request{Out: &out}); err != nil {
		t.Fatalf("the compatible fallback should have succeeded: %v", err)
	}
	if out.Word != "book" {
		t.Errorf("decoded %+v", out)
	}
	if calls != 2 {
		t.Errorf("made %d calls, want 2 (with the control, then without)", calls)
	}
}

func TestCompleteRetriesServerErrorsOnce(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"boom","code":"server_error"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"`+goodJSON+`"}}]}`)
	})

	var out payload
	if err := c.Complete(context.Background(), Request{Out: &out}); err != nil {
		t.Fatalf("a 5xx should be retried once: %v", err)
	}
	if calls != 2 {
		t.Errorf("made %d calls, want 2", calls)
	}
}

func TestCompleteReportsEmptyChoices(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	})
	var out payload
	if err := c.Complete(context.Background(), Request{Out: &out}); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("want ErrBadResponse, got %v", err)
	}
}

func TestCompleteRetriesWhenTheValidatorRejectsAReply(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, `{"choices":[{"message":{"content":"{\"word\":\"book\"}"}}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"`+goodJSON+`"}}]}`)
	})

	var out payload
	err := c.Complete(context.Background(), Request{
		Out: &out,
		Validate: func() error {
			if out.IPA == "" {
				return errors.New("the model forgot the ipa")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("the retry satisfied the validator, so it should have succeeded: %v", err)
	}
	if calls != 2 {
		t.Errorf("made %d calls, want 2", calls)
	}
}

func TestCompleteDoesNotInheritFieldsAcrossAttempts(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, `{"choices":[{"message":{"content":"`+goodJSON+`"}}]}`)
			return
		}
		// The second reply omits ipa. If the first reply's value survived into
		// this attempt, the validator below would wrongly accept it.
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"word\":\"book\"}"}}]}`)
	})

	var out payload
	attempts := 0
	err := c.Complete(context.Background(), Request{
		Out: &out,
		Validate: func() error {
			attempts++
			if attempts == 1 {
				return errors.New("reject the first reply on purpose")
			}
			if out.IPA == "" {
				return errors.New("missing ipa")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("a reply missing a field must not validate on the strength of the previous reply")
	}
	if calls != 2 {
		t.Errorf("made %d calls, want 2", calls)
	}
}

func TestContextCancellationIsNotRetried(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"choices":[{"message":{"content":"`+goodJSON+`"}}]}`)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out payload
	if err := c.Complete(ctx, Request{Out: &out}); err == nil {
		t.Fatal("expected a cancellation error")
	}
	if calls != 0 {
		t.Errorf("made %d calls, want 0 for an already-cancelled context", calls)
	}
}
