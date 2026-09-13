// Package llm is a small client for DeepSeek-compatible chat completions.
//
// The project asks a model to do two different jobs: look a word up to build
// the anchor layer, and build practice material around a word for the expansion
// layer. Both need the same authentication, the same retry policy, the same
// JSON extraction and the same reasoning-control fallback, so that machinery
// lives here once and the two callers contribute only a prompt and a validator.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"
)

// DefaultBaseURL is the OpenAI-compatible DeepSeek endpoint.
const DefaultBaseURL = "https://api.deepseek.com/v1"

// DefaultModel is the fastest and cheapest model. Both jobs are short,
// well-specified and low-stakes, which is exactly what a flash model is for.
const DefaultModel = "deepseek-v4-flash"

// ErrBadResponse means the model answered, but not with something usable. It is
// retried once, because a single malformed reply says nothing about the next
// one.
var ErrBadResponse = errors.New("model response was not usable")

// APIError is a non-2xx response from the API.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("api returned %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("api returned %d: %s", e.StatusCode, e.Message)
}

// Retryable reports whether repeating the identical request could plausibly
// succeed. Client errors other than rate limiting will not.
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// Client is a minimal chat-completions client.
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

// Request is one structured completion.
type Request struct {
	// System is the instruction that fixes the output contract.
	System string
	// User is the payload, normally the word being worked on.
	User string
	// Out receives the decoded JSON object.
	Out any
	// Validate runs after decoding. A non-nil error counts as a bad response,
	// so a reply that parses but violates the contract is retried instead of
	// being handed to the caller to reject.
	Validate func() error
}

// Complete asks the model for a JSON object and decodes it into req.Out.
//
// Failures that could plausibly be flukes — transport errors, 429, 5xx, or a
// reply that does not satisfy the contract — are attempted exactly once more.
// Everything else fails fast, because burning paid requests on a permanent
// error helps nobody.
func (c *Client) Complete(ctx context.Context, req Request) error {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		err := c.attempt(ctx, req, true)
		if err == nil {
			return nil
		}

		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
			// The reasoning control itself was rejected by this deployment.
			// Retry once without it rather than failing the request.
			return c.attempt(ctx, req, false)
		}
		if !retryable(err) {
			return err
		}

		lastErr = err
		if attempt == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	return lastErr
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
	// A cancelled or expired context is the caller's decision, not a fluke.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr)
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

func (c *Client) attempt(ctx context.Context, req Request, withThinkingControl bool) error {
	body := chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		ResponseFormat: &responseFormat{Type: "json_object"},
		Temperature:    0.3,
		MaxTokens:      1024,
	}
	if withThinkingControl {
		// These jobs are deterministic and short; reasoning tokens would be
		// paid for and then thrown away.
		body.Thinking = &thinkingControl{Type: "disabled"}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
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
		return apiErr
	}

	var parsed chatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("%w: %v", ErrBadResponse, err)
	}
	if len(parsed.Choices) == 0 {
		return fmt.Errorf("%w: response contained no choices", ErrBadResponse)
	}

	object, err := extractJSONObject(parsed.Choices[0].Message.Content)
	if err != nil {
		return err
	}
	zero(req.Out)
	if err := json.Unmarshal(object, req.Out); err != nil {
		return fmt.Errorf("%w: %v", ErrBadResponse, err)
	}
	if req.Validate != nil {
		if err := req.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrBadResponse, err)
		}
	}
	return nil
}

// zero clears a decode destination before each attempt.
//
// encoding/json only overwrites the fields a reply actually contains, so
// decoding a second reply on top of the first could leave a stale field behind
// and turn an incomplete answer into a plausible-looking one.
func zero(out any) {
	v := reflect.ValueOf(out)
	if v.Kind() == reflect.Pointer && !v.IsNil() && v.Elem().CanSet() {
		v.Elem().Set(reflect.Zero(v.Elem().Type()))
	}
}

// extractJSONObject pulls the JSON object out of a model reply, tolerating
// markdown fences and surrounding prose.
func extractJSONObject(content string) ([]byte, error) {
	text := strings.TrimSpace(content)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("%w: no JSON object in the reply", ErrBadResponse)
	}
	return []byte(text[start : end+1]), nil
}
