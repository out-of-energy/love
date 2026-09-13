// Package ai builds the expansion layer for a word: the ordinary-language
// meaning, example sentences, a scene, and a short dialogue.
//
// This layer answers a different question from the anchor layer. The anchor
// (ipa/eli5/chinese) answers "what is this word?" and must be absorbed in one
// glance. This answers "how do I use this word?" and is read afterwards, once
// recall has already succeeded.
//
// It is generated once per word and cached. A word whose practice material
// changed every day would stop being a memory anchor and become a fresh reading
// exercise, which is the opposite of what review is for.
package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/out-of-energy/love/internal/llm"
)

// Line is one turn of a short conversation.
type Line struct {
	Speaker string `json:"speaker"`
	Line    string `json:"line"`
}

// Content is the expansion layer for one word.
type Content struct {
	// Meaning is the ordinary adult phrasing. It is deliberately NOT the same
	// register as the anchor's eli5: the anchor is written for a three-year-old
	// so it can be read instantly, and this is the fuller version shown second.
	Meaning  string   `json:"meaning"`
	Examples []string `json:"examples"`
	Scene    string   `json:"scene"`
	Dialogue []Line   `json:"dialogue"`
}

// UsesWord reports whether text contains word, ignoring case. Inflections match
// too ("maintain" matches "maintains"), which is what a learner actually needs
// to see.
func UsesWord(text, word string) bool {
	text, word = strings.ToLower(text), strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return false
	}
	return strings.Contains(text, word)
}

// Validate enforces the content contract for a given word.
//
// The dialogue rule is the one that matters. A model will happily produce a
// definition, an example and a scene while the conversation never uses the
// target word at all — and a dialogue that does not contain the word teaches
// nothing, because its entire purpose is to show the word being used by
// somebody in a real exchange.
//
// storage enforces the same contract again at the persistence boundary. The
// duplication is deliberate: this check exists so a bad reply can be retried
// while the request is still in flight, and that check exists so bad content
// can never reach the user's cache even if it arrives by another path.
func (c Content) Validate(word string) error {
	switch {
	case strings.TrimSpace(c.Meaning) == "":
		return fmt.Errorf("missing meaning")
	case len(c.Examples) == 0:
		return fmt.Errorf("no examples")
	case len(c.Dialogue) == 0:
		return fmt.Errorf("no dialogue")
	}
	for i, line := range c.Dialogue {
		if strings.TrimSpace(line.Line) == "" {
			return fmt.Errorf("dialogue line %d is empty", i+1)
		}
	}
	for _, line := range c.Dialogue {
		if UsesWord(line.Line, word) {
			return nil
		}
	}
	return fmt.Errorf("no dialogue line uses %q", word)
}

// Provider generates expansion content for a word.
//
// v0.3 ships one implementation. The interface exists so a local model can be
// added later without touching the engine.
type Provider interface {
	GenerateContent(ctx context.Context, word string) (Content, error)
	Name() string
}

const systemPrompt = `You write practice material for an English learner who already
knows the basic meaning of a word and now needs to learn to use it.

The user sends one English word or short phrase. Reply with ONLY a JSON object
with exactly these fields:

{"meaning":"...","examples":["..."],"scene":"...","dialogue":[{"speaker":"A","line":"..."}]}

Rules:
- "meaning": the meaning in ordinary adult English. This is NOT a
  child-simple explanation; write it the way a dictionary for learners would.
- "examples": one to three natural sentences that use the word.
- "scene": one short sentence describing the situation those sentences happen in.
- "dialogue": two to four turns of a short, natural conversation between two
  people. At least one line MUST contain the word itself. Without the word the
  dialogue is useless, so check this before answering.
- Keep everything in plain, everyday English. No markdown, no code fences, no
  commentary. Output the JSON object only.`

// DeepSeek generates expansion content through a DeepSeek-compatible API.
type DeepSeek struct{ llm *llm.Client }

// NewDeepSeek builds a generator with a per-request timeout. The timeout is
// longer than a lookup's, because this reply is several times larger.
func NewDeepSeek(apiKey, baseURL, model string, timeout time.Duration) *DeepSeek {
	return &DeepSeek{llm: llm.NewClient(apiKey, baseURL, model, timeout)}
}

// Name identifies the provider, and is recorded alongside the cached content so
// a later switch to another model is visible in the data.
func (d *DeepSeek) Name() string { return "deepseek" }

// GenerateContent asks the model for the expansion layer of word.
func (d *DeepSeek) GenerateContent(ctx context.Context, word string) (Content, error) {
	var content Content
	err := d.llm.Complete(ctx, llm.Request{
		System: systemPrompt,
		User:   word,
		Out:    &content,
		Validate: func() error {
			return content.Validate(word)
		},
	})
	if err != nil {
		return Content{}, err
	}
	return content, nil
}
