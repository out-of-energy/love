// Package dict asks a model for the anchor layer of a word: its IPA, a
// deliberately child-simple English explanation, and one common Chinese
// meaning.
//
// This is the layer a learner reads in a single glance during review, and it is
// written once and never regenerated. The expansion layer — example sentences
// and dialogue — lives in package ai, because it answers a different question
// (how do I use this word?) at a different moment (after recall succeeds).
package dict

import (
	"context"
	"fmt"
	"time"

	"github.com/out-of-energy/love/internal/llm"
)

// Defaults are re-exported so callers need only one import for configuration.
const (
	DefaultBaseURL = llm.DefaultBaseURL
	DefaultModel   = llm.DefaultModel
)

// ErrBadResponse and APIError are re-exported for the same reason.
var ErrBadResponse = llm.ErrBadResponse

// APIError is a non-2xx response from the API.
type APIError = llm.APIError

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

// Entry is the anchor layer for one word.
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

// Client looks words up.
type Client struct{ llm *llm.Client }

// NewClient builds a lookup client with a per-request timeout.
func NewClient(apiKey, baseURL, model string, timeout time.Duration) *Client {
	return &Client{llm: llm.NewClient(apiKey, baseURL, model, timeout)}
}

// Generate asks the model for the anchor layer of word.
//
// word must already be the canonical normalized key; it is echoed into the
// result rather than taken from the model, so the model cannot invent a second
// key for a word the caller has already identified.
func (c *Client) Generate(ctx context.Context, word string) (Entry, error) {
	var entry Entry
	err := c.llm.Complete(ctx, llm.Request{
		System: systemPrompt,
		User:   word,
		Out:    &entry,
		Validate: func() error {
			entry.Word = word
			return entry.Validate()
		},
	})
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}
