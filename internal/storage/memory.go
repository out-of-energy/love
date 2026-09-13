package storage

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/out-of-energy/love/internal/memory"
)

// DateLayout is the calendar-date format used inside memory.json. Scheduling is
// day-granular, so writing a full timestamp would imply a precision the
// algorithm does not have and would make two equivalent caches differ.
const DateLayout = "2006-01-02"

// memoryVersion is bumped whenever the cache shape changes, so an old file is
// recognised as stale instead of being misread.
const memoryVersion = 1

// MemoryItem is one word's cached state as written to disk.
type MemoryItem struct {
	Box        int    `json:"box"`
	NextReview string `json:"next_review"`
	LastReview string `json:"last_review,omitempty"`
}

// MemoryFile is the derived state cache.
//
// It is never a source of truth. Deleting it must cost nothing but a rebuild
// from reviews.jsonl, and GeneratedFrom exists so the system can tell on its
// own that the cache no longer matches its inputs.
type MemoryFile struct {
	Version       int                   `json:"version"`
	GeneratedFrom string                `json:"generated_from"`
	Items         map[string]MemoryItem `json:"items"`
}

// NewMemoryFile converts derived states into the on-disk shape.
func NewMemoryFile(states map[string]memory.State, from string) *MemoryFile {
	mf := &MemoryFile{
		Version:       memoryVersion,
		GeneratedFrom: from,
		Items:         make(map[string]MemoryItem, len(states)),
	}
	for id, st := range states {
		item := MemoryItem{
			Box:        st.Box,
			NextReview: st.NextReview.Format(DateLayout),
		}
		if !st.LastReview.IsZero() {
			item.LastReview = st.LastReview.Format(DateLayout)
		}
		mf.Items[id] = item
	}
	return mf
}

// States converts the cache back into engine states.
//
// A malformed cache is an error rather than a silent zero value: a corrupt date
// silently read as the zero time would make every word look overdue and dump
// the entire collection into one day's session.
func (mf *MemoryFile) States() (map[string]memory.State, error) {
	out := make(map[string]memory.State, len(mf.Items))
	for id, item := range mf.Items {
		next, err := time.Parse(DateLayout, item.NextReview)
		if err != nil {
			return nil, fmt.Errorf("word %s: bad next_review %q", id, item.NextReview)
		}
		st := memory.State{Box: item.Box, NextReview: next}
		if item.LastReview != "" {
			last, err := time.Parse(DateLayout, item.LastReview)
			if err != nil {
				return nil, fmt.Errorf("word %s: bad last_review %q", id, item.LastReview)
			}
			st.LastReview = last
		}
		out[id] = st
	}
	return out, nil
}

// Stale reports whether the cache was built from different inputs than the ones
// currently on disk, or was written by a different version of the schema.
func (mf *MemoryFile) Stale(currentFingerprint string) bool {
	return mf.Version != memoryVersion || mf.GeneratedFrom != currentFingerprint
}

// SaveMemory writes the cache atomically.
func SaveMemory(path string, mf *MemoryFile) error {
	data, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

// LoadMemory reads the cache.
func LoadMemory(path string) (*MemoryFile, error) {
	data, err := readFileOrEmpty(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no memory cache at %s", path)
	}
	var mf MemoryFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("memory cache at %s is unreadable: %w", path, err)
	}
	if mf.Items == nil {
		mf.Items = map[string]MemoryItem{}
	}
	return &mf, nil
}

// Fingerprint identifies the current inputs a cache would be built from.
func Fingerprint(paths Paths) (string, error) {
	words, err := readFileOrEmpty(paths.Words)
	if err != nil {
		return "", err
	}
	reviews, err := readFileOrEmpty(paths.Reviews)
	if err != nil {
		return "", err
	}
	return fingerprint(words, reviews), nil
}

// Rebuild replays the event log into a fresh cache.
//
// This is the recovery path: it is what makes it safe for memory.json to be
// deleted, hand-edited or lost, and it is the operation the event-replay test
// asserts against the incremental state.
func Rebuild(cfg memory.Config, paths Paths) (*MemoryFile, map[string]memory.State, error) {
	reviews, _, err := OpenReviews(paths.Reviews).Load()
	if err != nil {
		return nil, nil, err
	}
	states := cfg.Reduce(reviews)

	from, err := Fingerprint(paths)
	if err != nil {
		return nil, nil, err
	}
	return NewMemoryFile(states, from), states, nil
}
