// Package cache implements the JSONL word store: normalization, tolerant
// reading, first-record-wins lookup, and deduplicated appends.
//
// The word file is deliberately plain text with one JSON object per line so it
// stays readable, greppable and hand-editable. Because people do edit it by
// hand, reading tolerates damaged lines instead of failing the whole lookup.
package cache

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Record is one dictionary entry. These four fields are the entire schema;
// nothing else is ever written to the word file.
type Record struct {
	Word    string `json:"word"`
	IPA     string `json:"ipa"`
	ELI5    string `json:"eli5"`
	Chinese string `json:"chinese"`
}

// Validate reports whether a record is complete enough to be worth storing.
func (r Record) Validate() error {
	switch {
	case r.Word == "":
		return fmt.Errorf("missing word")
	case r.IPA == "":
		return fmt.Errorf("missing ipa")
	case r.ELI5 == "":
		return fmt.Errorf("missing eli5")
	case r.Chinese == "":
		return fmt.Errorf("missing chinese")
	}
	return nil
}

// Normalize turns raw input into the canonical lookup key: lowercased, with
// surrounding and repeated whitespace collapsed. Internal hyphens and single
// spaces survive so that phrases such as "ice cream" and "well-known" work.
func Normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// DefaultPath is the user-level word file. It lives directly under the home
// directory so that it is always writable by the user and never inside a
// sandboxed workspace.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate home directory: %w", err)
	}
	return filepath.Join(home, ".ewh", "words.jsonl"), nil
}

// Load reads every valid record in file order. A missing file is not an error:
// it simply means the dictionary is still empty.
//
// Unparseable lines and records without a word are skipped and reported as
// human-readable warnings rather than aborting, so one bad line can never make
// the rest of the dictionary unreachable.
func Load(path string) ([]Record, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer f.Close()

	var (
		records  []Record
		warnings []string
	)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(text), &rec); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping invalid JSON", path, line))
			continue
		}
		rec.Word = Normalize(rec.Word)
		if rec.Word == "" {
			warnings = append(warnings, fmt.Sprintf("%s:%d: skipping record without a word", path, line))
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return records, warnings, err
	}
	return records, warnings, nil
}

// Lookup returns the first record for word. First-wins is what keeps the
// "one word, one record" invariant intact even if the file already contains
// duplicates from an earlier tool.
func Lookup(records []Record, word string) (Record, bool) {
	for _, rec := range records {
		if rec.Word == word {
			return rec, true
		}
	}
	return Record{}, false
}

// Save stores rec under the write lock and returns the canonical record for
// that word: the one already on disk if another run stored it first, otherwise
// rec itself. written reports whether this call actually appended a line.
//
// Callers must generate the record *before* calling Save so the lock is never
// held across a network request.
func Save(path string, rec Record) (canonical Record, written bool, err error) {
	release, err := lock(path, 30*time.Second)
	if err != nil {
		return Record{}, false, err
	}
	defer release()

	records, _, err := Load(path)
	if err != nil {
		return Record{}, false, err
	}
	if existing, ok := Lookup(records, rec.Word); ok {
		return existing, false, nil
	}
	if err := appendLine(path, rec); err != nil {
		return Record{}, false, err
	}
	return rec, true, nil
}

// appendLine adds exactly one newline-terminated JSON object.
func appendLine(path string, rec Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	// A single Write of one short line under O_APPEND cannot be interleaved
	// with another process's write, so two writers never produce a torn line.
	payload := append(line, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(payload); err != nil {
		return err
	}
	return nil
}

// lock serializes the read-check-append sequence across processes using an
// exclusively created sidecar file. This keeps the tool free of
// platform-specific syscalls and third-party dependencies.
func lock(path string, timeout time.Duration) (func(), error) {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			var once sync.Once
			return func() { once.Do(func() { os.Remove(lockPath) }) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		// Recover from a lock left behind by a process that was killed.
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for write lock %s", lockPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
