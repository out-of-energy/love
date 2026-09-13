package storage

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/out-of-energy/love/internal/ai"
)

// DialogueLine and Content are aliases, not copies.
//
// The expansion contract belongs to package ai, which is where the model's
// output shape is defined and enforced. Storage persists that shape; defining a
// second identical struct here would let the two drift, and a drift between
// what is generated and what is stored is exactly the kind of bug that shows up
// as missing fields months later.
type DialogueLine = ai.Line

// Content is the expansion layer for one word.
type Content = ai.Content

// Generated is the cached expansion for one word.
//
// It is a cache, not an asset: regenerating it costs an API call and nothing
// else. The anchor layer (ipa/eli5/chinese) lives in words.jsonl precisely
// because it must never change, and this must be free to.
type Generated struct {
	WordID    string    `json:"word_id"`
	Word      string    `json:"word"`
	Provider  string    `json:"provider"`
	Content   Content   `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// UsesWord reports whether text contains the word, ignoring case.
//
// It delegates to package ai so that the rule deciding whether a dialogue is
// usable is written once, in the package that states the contract.
func UsesWord(text, word string) bool { return ai.UsesWord(text, word) }

// Validate reports whether the record is complete and its content satisfies the
// expansion contract.
//
// The content rules live in package ai. This checks only what is storage's
// business — that the record has an identity — and then hands the content to
// the package that owns its shape.
func (g Generated) Validate() error {
	switch {
	case g.WordID == "":
		return fmt.Errorf("missing word_id")
	case g.Word == "":
		return fmt.Errorf("missing word")
	}
	return g.Content.Validate(g.Word)
}

// GeneratedStore reads and writes generated.jsonl.
type GeneratedStore struct{ path string }

// OpenGenerated returns a store backed by path.
func OpenGenerated(path string) *GeneratedStore { return &GeneratedStore{path: path} }

// Path returns the backing file.
func (s *GeneratedStore) Path() string { return s.path }

// Load returns every cached record in file order.
//
// Records are not validated on load: content that was accepted once must remain
// readable even if the contract tightens later, otherwise tightening the rules
// would silently discard work the user already paid for.
func (s *GeneratedStore) Load() ([]Generated, []string, error) {
	lines, err := readLines(s.path)
	if err != nil {
		return nil, nil, err
	}

	var (
		items    []Generated
		warnings []string
		seen     = make(map[string]bool, len(lines))
	)
	for i, line := range lines {
		var g Generated
		if err := json.Unmarshal(line, &g); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping invalid JSON", s.path, i+1))
			continue
		}
		if g.WordID == "" {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping record without a word_id", s.path, i+1))
			continue
		}
		if seen[g.WordID] {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping duplicate content for %s", s.path, i+1, g.WordID))
			continue
		}
		seen[g.WordID] = true
		items = append(items, g)
	}
	return items, warnings, nil
}

// Find returns the cached expansion for wordID.
func FindGenerated(items []Generated, wordID string) (Generated, bool) {
	for _, g := range items {
		if g.WordID == wordID {
			return g, true
		}
	}
	return Generated{}, false
}

// Save stores g unless wordID already has content, in which case the existing
// record is returned untouched.
//
// Caching is what keeps the anchor layer stable: a word whose expansion changed
// every day would turn review into reading fresh prose rather than recalling a
// known thing. It also means the API is called once per word, not once per
// review.
func (s *GeneratedStore) Save(g Generated, now time.Time) (Generated, bool, error) {
	if g.WordID == "" {
		return Generated{}, false, fmt.Errorf("cannot cache content without a word_id")
	}
	if err := g.Validate(); err != nil {
		return Generated{}, false, fmt.Errorf("refusing to cache invalid content: %w", err)
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}

	var (
		saved   Generated
		created bool
	)
	err := withLock(s.path, 30*time.Second, func() error {
		items, _, err := s.Load()
		if err != nil {
			return err
		}
		if existing, ok := FindGenerated(items, g.WordID); ok {
			saved, created = existing, false
			return nil
		}
		line, err := json.Marshal(g)
		if err != nil {
			return err
		}
		if err := appendLine(s.path, line); err != nil {
			return err
		}
		saved, created = g, true
		return nil
	})
	return saved, created, err
}
