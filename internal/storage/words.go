package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Normalize turns raw input into the canonical comparison key: lowercased, with
// surrounding and repeated whitespace collapsed. Internal hyphens and single
// spaces survive, so phrases such as "ice cream" keep working.
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// Word is one entry in words.jsonl: a language asset the user owns.
//
// Both the original spelling and the normalized key are stored. Word preserves
// what the user typed, so the file they read back does not silently lowercase
// everything; Normalized is what deduplication compares. Persisting the key
// rather than recomputing it means a future change to Normalize cannot
// re-partition existing data behind the user's back.
type Word struct {
	ID         string    `json:"id"`
	Word       string    `json:"word"`
	Normalized string    `json:"normalized"`
	IPA        string    `json:"ipa"`
	ELI5       string    `json:"eli5"`
	Chinese    string    `json:"chinese"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
}

// NeedsMigration reports whether w predates the current schema.
func (w Word) NeedsMigration() bool {
	return w.ID == "" || w.Normalized == "" || w.Source == "" || w.CreatedAt.IsZero()
}

// Find returns the word whose normalized form matches key.
func Find(words []Word, key string) (Word, bool) {
	key = Normalize(key)
	for _, w := range words {
		if w.Normalized == key {
			return w, true
		}
	}
	return Word{}, false
}

// WordStore reads and writes words.jsonl.
type WordStore struct{ path string }

// OpenWords returns a store backed by path.
func OpenWords(path string) *WordStore { return &WordStore{path: path} }

// Path returns the backing file.
func (s *WordStore) Path() string { return s.path }

// Load returns every word in file order.
//
// Legacy four-field records (word/ipa/eli5/chinese) load without complaint and
// are flagged for migration rather than rejected. A user's existing dictionary
// must never become unreadable just because the schema grew.
//
// Duplicate normalized words collapse to the first occurrence, with a warning.
// A word that entered the scheduler twice would consume two shares of the daily
// budget and its two states would drift apart, so this is a correctness
// guarantee rather than tidiness.
func (s *WordStore) Load() ([]Word, []string, error) {
	lines, err := readLines(s.path)
	if err != nil {
		return nil, nil, err
	}

	var (
		words    []Word
		warnings []string
		seen     = make(map[string]bool, len(lines))
	)
	for i, line := range lines {
		var w Word
		if err := json.Unmarshal(line, &w); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping invalid JSON", s.path, i+1))
			continue
		}
		w.Word = strings.TrimSpace(w.Word)
		if w.Normalized == "" {
			w.Normalized = Normalize(w.Word)
		}
		if w.Normalized == "" {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping record without a word", s.path, i+1))
			continue
		}
		if seen[w.Normalized] {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping duplicate %q", s.path, i+1, w.Normalized))
			continue
		}
		seen[w.Normalized] = true
		words = append(words, w)
	}
	return words, warnings, nil
}

// AddResult reports what Add did.
type AddResult struct {
	Word    Word
	Created bool
}

// Add stores w unless its normalized form is already present, in which case the
// existing record is returned untouched.
//
// This is the specification's uniqueness constraint expressed as behaviour
// instead of prose: one word, one record, forever. Repeated adds are idempotent,
// so importing the same file twice cannot double a word's share of the study
// budget. The check and the append happen under one lock, so two concurrent
// adds cannot both decide they are first.
func (s *WordStore) Add(w Word, now time.Time) (AddResult, error) {
	w.Word = strings.TrimSpace(w.Word)
	if w.Normalized == "" {
		w.Normalized = Normalize(w.Word)
	}
	if w.Normalized == "" {
		return AddResult{}, fmt.Errorf("cannot add a word without a name")
	}

	var result AddResult
	err := withLock(s.path, 30*time.Second, func() error {
		words, _, err := s.Load()
		if err != nil {
			return err
		}
		if existing, ok := Find(words, w.Normalized); ok {
			result = AddResult{Word: existing, Created: false}
			return nil
		}

		if w.ID == "" {
			id, err := uniqueID(words)
			if err != nil {
				return err
			}
			w.ID = id
		}
		if w.Source == "" {
			w.Source = "cli"
		}
		if w.CreatedAt.IsZero() {
			w.CreatedAt = now
		}

		line, err := json.Marshal(w)
		if err != nil {
			return err
		}
		if err := appendLine(s.path, line); err != nil {
			return err
		}
		result = AddResult{Word: w, Created: true}
		return nil
	})
	return result, err
}

// Rewrite replaces the whole file atomically, preserving order.
func (s *WordStore) Rewrite(words []Word) error {
	var buf bytes.Buffer
	for _, w := range words {
		line, err := json.Marshal(w)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return writeFileAtomic(s.path, buf.Bytes(), 0o644)
}

// MigrationReport describes what a migration changed.
type MigrationReport struct {
	Total     int
	Migrated  int
	Backup    string
	Performed bool
}

// Migrate upgrades legacy records to the current schema, in place.
//
// The original file is copied aside first. This rewrites a file the user may
// have spent years accumulating, and a schema change is exactly the moment when
// the tool should not be the only copy of it.
//
// Legacy records carry no creation time; the file's own modification time is a
// better estimate than "now", which would claim every historical word was added
// today and destroy the ordering that decides which new words are introduced
// first.
func (s *WordStore) Migrate(now time.Time) (MigrationReport, error) {
	words, warnings, err := s.Load()
	if err != nil {
		return MigrationReport{}, err
	}

	report := MigrationReport{Total: len(words)}
	if len(warnings) > 0 {
		// Rewriting would silently discard the damaged lines, and those lines
		// may be the only copy of a word. Refuse and let the user look at them.
		return report, fmt.Errorf("%s has %d damaged line(s); refusing to rewrite it", s.path, len(warnings))
	}
	for _, w := range words {
		if w.NeedsMigration() {
			report.Migrated++
		}
	}
	if report.Migrated == 0 {
		return report, nil
	}

	created := now
	if info, statErr := os.Stat(s.path); statErr == nil {
		created = info.ModTime()
	}

	used := make(map[string]bool, len(words))
	for i := range words {
		for words[i].ID == "" || used[words[i].ID] {
			id, err := uniqueID(words)
			if err != nil {
				return report, err
			}
			words[i].ID = id
		}
		used[words[i].ID] = true

		if words[i].Normalized == "" {
			words[i].Normalized = Normalize(words[i].Word)
		}
		if words[i].Source == "" {
			words[i].Source = "cli"
		}
		if words[i].CreatedAt.IsZero() {
			words[i].CreatedAt = created
		}
	}

	original, err := readFileOrEmpty(s.path)
	if err != nil {
		return report, err
	}
	backup := fmt.Sprintf("%s.bak-%s", s.path, now.Format("20060102-150405"))
	if err := os.WriteFile(backup, original, 0o600); err != nil {
		return report, err
	}
	if err := s.Rewrite(words); err != nil {
		return report, err
	}

	report.Backup = backup
	report.Performed = true
	return report, nil
}

// uniqueID draws identifiers until one is unused.
func uniqueID(existing []Word) (string, error) {
	used := make(map[string]bool, len(existing))
	for _, w := range existing {
		used[w.ID] = true
	}
	for i := 0; i < 100; i++ {
		id, err := newID()
		if err != nil {
			return "", err
		}
		if !used[id] {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique word id")
}
