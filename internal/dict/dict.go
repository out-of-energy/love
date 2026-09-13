// Package dict turns one word into one dictionary record by calling the
// DeepSeek chat-completions API.
//
// The API key is passed in from the caller and is only ever used as a request
// header: it is never logged, never written to disk, and never included in any
// error message this package produces.
package dict

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the OpenAI-compatible DeepSeek endpoint.
const DefaultBaseURL = "https://api.deepseek.com/v1"

// DefaultModel is the fastest and cheapest model, which is all a single word
// lookup needs. The legacy deepseek-chat/deepseek-reasoner ids are deprecated,
// so we default to the current flash model.
const DefaultModel = "deepseek-v4-flash"

// systemPrompt encodes the product's whole editorial stance: one word, one
// meaning, explained so simply that a small child would follow it.
const systemPrompt = `You are a concise English word learning assistant.

The user sends one English word or short phrase. Reply with ONLY a JSON object
containing exactly these four string fields:

{"word":"...","ipa":"...","eli5":"...","chinese":"..."}

Rules:
- "word": the input word or phrase, lowercased, otherwise unchanged.
- "ipa": a standard IPA transcription wrapped in slashes, e.g. /ˈkjʊəriəs/.
- "eli5": explain the meaning in VERY simple English that a three-year-old could
  understand. Use common words and short sentences. Write "very, very big",
  never "extremely large in scale". Never use dictionary or academic wording.
- "chinese": the single most common Chinese meaning, a few characters. No part
  of speech, no numbered senses, no alternatives.
Output the JSON object only. No markdown, no code fences, no commentary.`

// ErrBadResponse means the model answered, but not with a usable record.
var ErrBadResponse = errors.New("model response was not a valid dictionary record")

// Entry is the content the model produced for one word.
//
// It is deliberately independent of any storage or scheduling type: this
// package talks to a model and nothing else, so a change to the word file's
// schema can never ripple in here.
type Entry struct {
	Word    string
	IPA     string
	ELI5    string
	Chinese string
}

// Validate reports whether the model returned everything we asked for.
func (e Entry) Validate() error {
	switch {
	case e.Word == "":
		return fmt.Errorf("missing word")
	case e.IPA == "":
		return fmt.Errorf("missing ipa")
	case e.ELI5 == "":
		return fmt.Errorf("missing eli5")
	case e.Chinese == "":
		return fmt.Errorf("missing chinese")
	}
	return nil
}

// APIError is a non-2xx response from the API.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("deepseek api returned %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("deepseek api returned %d: %s", e.StatusCode, e.Message)
}

// Retryable reports whether repeating the identical request could plausibly
// succeed. Client errors other than rate limiting will not.
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// Client is a minimal DeepSeek chat-completions client.
type Client struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

// NewClient builds a client with a per-request timeout.
func NewClient(apiKey, baseURL, model string, timeout time.Duration) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type thinkingControl struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string           `json:"model"`
	Messages       []chatMessage    `json:"messages"`
	ResponseFormat *responseFormat  `json:"response_format,omitempty"`
	Temperature    float64          `json:"temperature"`
	MaxTokens      int              `json:"max_tokens,omitempty"`
	Thinking       *thinkingControl `json:"thinking,omitempty"`
	Stream         bool             `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// Generate asks the model for the record of word.
//
// Lookups are deterministic and short, so reasoning is switched off to avoid
// paying for reasoning tokens we would only discard. Published docs disagree on
// the exact control name, so a request rejected with 400 is retried once
// without it rather than failing the lookup.
//
// Any failure that could plausibly be a fluke (transport error, 429, 5xx, or an
// unusable answer) is attempted exactly once more. Everything else fails fast,
// because burning paid requests on a permanent error helps nobody.
func (c *Client) Generate(ctx context.Context, word string) (Entry, error) {
	var lastErr error
	for try := 0; try < 2; try++ {
		rec, err := c.attempt(ctx, word, true)
		if err == nil {
			return rec, nil
		}

		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
			// The thinking control itself was rejected; retry without it.
			return c.attempt(ctx, word, false)
		}
		if !retryable(err) {
			return Entry{}, err
		}
		lastErr = err
		if try == 0 {
			select {
			case <-ctx.Done():
				return Entry{}, ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	return Entry{}, lastErr
}

// retryable reports whether repeating the request could plausibly succeed.
func retryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	if errors.Is(err, ErrBadResponse) {
		return true
	}
	// A cancelled or timed-out context is the caller's decision, not a fluke.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func (c *Client) attempt(ctx context.Context, word string, withThinkingControl bool) (Entry, error) {
	body := chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: word},
		},
		ResponseFormat: &responseFormat{Type: "json_object"},
		Temperature:    0.3,
		MaxTokens:      512,
	}
	if withThinkingControl {
		body.Thinking = &thinkingControl{Type: "disabled"}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Entry{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Entry{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Entry{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Entry{}, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		var envelope chatResponse
		if json.Unmarshal(data, &envelope) == nil && envelope.Error != nil {
			apiErr.Message = envelope.Error.Message
			if envelope.Error.Code != nil {
				apiErr.Code = fmt.Sprint(envelope.Error.Code)
			}
		}
		if apiErr.Message == "" {
			apiErr.Message = strings.TrimSpace(string(data))
		}
		return Entry{}, apiErr
	}

	var parsed chatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Entry{}, fmt.Errorf("%w: %v", ErrBadResponse, err)
	}
	if len(parsed.Choices) == 0 {
		return Entry{}, fmt.Errorf("%w: response contained no choices", ErrBadResponse)
	}
	return decodeRecord(parsed.Choices[0].Message.Content, word)
}

// decodeRecord extracts the JSON object the model was asked for, tolerating
// markdown fences and surrounding prose, then enforces the four-field schema.
func decodeRecord(content, word string) (Entry, error) {
	text := strings.TrimSpace(content)
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			text = text[start : end+1]
		}
	}

	var rec Entry
	if err := json.Unmarshal([]byte(text), &rec); err != nil {
		return Entry{}, fmt.Errorf("%w: %v", ErrBadResponse, err)
	}
	// The caller's spelling is authoritative: the model must not be able to
	// invent a second key for a word the caller already normalized.
	rec.Word = word
	if err := rec.Validate(); err != nil {
		return Entry{}, fmt.Errorf("%w: %v", ErrBadResponse, err)
	}
	return rec, nil
}
