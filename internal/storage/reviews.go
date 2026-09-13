package storage

import (
	"encoding/json"
	"fmt"

	"github.com/out-of-energy/love/internal/memory"
)

// ReviewStore appends to and reads reviews.jsonl, the immutable learning
// history.
type ReviewStore struct{ path string }

// OpenReviews returns a store backed by path.
func OpenReviews(path string) *ReviewStore { return &ReviewStore{path: path} }

// Path returns the backing file.
func (s *ReviewStore) Path() string { return s.path }

// Append records one event.
//
// No lock is taken: a single write of one short line under O_APPEND is already
// atomic, and the history is append-only, so there is no read-modify-write
// sequence to protect. Every event ever recorded stays in this file.
func (s *ReviewStore) Append(r memory.Review) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return appendLine(s.path, line)
}

// Load returns every event in file order.
//
// Damaged lines are skipped with a warning. Losing one event costs a little
// schedule accuracy; refusing to load the file would cost the entire history,
// which is the only thing in this system that cannot be regenerated.
func (s *ReviewStore) Load() ([]memory.Review, []string, error) {
	lines, err := readLines(s.path)
	if err != nil {
		return nil, nil, err
	}

	var (
		reviews  []memory.Review
		warnings []string
	)
	for i, line := range lines {
		var r memory.Review
		if err := json.Unmarshal(line, &r); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping invalid JSON", s.path, i+1))
			continue
		}
		if r.WordID == "" {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping event without a word id", s.path, i+1))
			continue
		}
		reviews = append(reviews, r)
	}
	return reviews, warnings, nil
}
