// Package storage owns the on-disk shape of the three files that make up a
// personal language store:
//
//	words.jsonl    the user's language assets, append-only in practice
//	reviews.jsonl  the immutable learning history, the only source of truth
//	memory.json    a derived cache that can always be rebuilt from the two above
//
// Two rules shape everything here. Writes must survive a crash, because losing
// a year of learning history to a half-written file is unacceptable. And reads
// must tolerate damage, because these are plain text files that people will
// edit, merge and restore from backups.
package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Paths locates the files that make up a store.
type Paths struct {
	Words     string
	Reviews   string
	Memory    string
	Generated string
}

// DefaultPaths returns the conventional locations under the user's home
// directory.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("cannot locate home directory: %w", err)
	}
	return PathsIn(filepath.Join(home, ".ewh")), nil
}

// PathsIn returns the store layout rooted at dir.
func PathsIn(dir string) Paths {
	return Paths{
		Words:     filepath.Join(dir, "words.jsonl"),
		Reviews:   filepath.Join(dir, "reviews.jsonl"),
		Memory:    filepath.Join(dir, "memory.json"),
		Generated: filepath.Join(dir, "generated.jsonl"),
	}
}

// writeFileAtomic replaces path with data so that a crash can never leave a
// truncated file behind.
//
// The bytes are written to a temporary file in the same directory, flushed to
// disk, and then renamed over the target. Rename is atomic within a filesystem,
// so a reader sees either the whole old file or the whole new one. Writing the
// temp file in the same directory is what makes the rename atomic; using the
// system temp directory would silently degrade this into a copy.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Without the sync, a power loss can leave the rename durable but the
	// contents empty on some filesystems.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = "" // renamed successfully; nothing left to clean up
	return nil
}

// appendLine adds one newline-terminated line to path.
//
// A single write of a short buffer under O_APPEND cannot be interleaved with
// another process's write, so two concurrent appends never produce a torn line.
// This is the only reason a review event can be recorded without rewriting the
// whole history.
func appendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload := make([]byte, 0, len(line)+1)
	payload = append(payload, line...)
	payload = append(payload, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(payload)
	return err
}

// readFileOrEmpty returns a file's bytes, treating absence as empty rather than
// as an error, because every file in the store may legitimately not exist yet.
func readFileOrEmpty(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// readLines returns the non-empty lines of a file. A missing file is not an
// error: it simply means the store is still empty.
func readLines(path string) ([][]byte, error) {
	data, err := readFileOrEmpty(path)
	if err != nil {
		return nil, err
	}

	var lines [][]byte
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			if line := trimSpace(data[start:i]); len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	return lines, nil
}

func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isSpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}

// withLock serializes a read-modify-write sequence across processes using an
// exclusively created sidecar file. This keeps the tool free of
// platform-specific syscalls and third-party dependencies.
func withLock(path string, timeout time.Duration, fn func() error) error {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			defer os.Remove(lockPath)
			return fn()
		}
		if !os.IsExist(err) {
			return err
		}
		// Recover from a lock left behind by a process that was killed.
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for write lock %s", lockPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// fingerprint identifies the exact inputs a memory cache was built from.
//
// It covers words as well as reviews, because deleting or adding a word changes
// which words are eligible to be introduced and therefore changes the derived
// state. Hashing only the review log would leave the cache stale after a word
// file edit.
func fingerprint(parts ...[]byte) string {
	h := sha256.New()
	for i, p := range parts {
		fmt.Fprintf(h, "part-%d:%d\n", i, len(p))
		h.Write(p)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16]
}

// newID returns a short random identifier. Eight hex characters keep the word
// file readable while making collisions vanishingly unlikely at personal scale.
func newID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
