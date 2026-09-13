package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/out-of-energy/love/internal/memory"
)

func review(id, wordID string, rating memory.Rating, at time.Time) memory.Review {
	return memory.Review{
		ID:     id,
		WordID: wordID,
		Time:   at,
		Rating: rating,
	}
}

func TestReviewAppendAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.jsonl")
	store := OpenReviews(path)

	events := []memory.Review{
		review("r1", "w1", memory.Good, testNow),
		review("r2", "w1", memory.Easy, testNow.AddDate(0, 0, 3)),
		review("r3", "w2", memory.Again, testNow.AddDate(0, 0, 1)),
	}
	for _, e := range events {
		if err := store.Append(e); err != nil {
			t.Fatal(err)
		}
	}

	got, warnings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(got) != len(events) {
		t.Fatalf("got %d events, want %d", len(got), len(events))
	}
	for i := range events {
		if got[i].ID != events[i].ID || got[i].Rating != events[i].Rating || got[i].WordID != events[i].WordID {
			t.Errorf("event %d = %+v, want %+v", i, got[i], events[i])
		}
		if !got[i].Time.Equal(events[i].Time) {
			t.Errorf("event %d time = %v, want %v", i, got[i].Time, events[i].Time)
		}
	}
}

func TestReviewLoadSkipsDamagedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.jsonl")
	writeFile(t, path, `{"id":"r1","word_id":"w1","time":"2026-09-13T10:00:00Z","rating":3}`+"\n"+
		"not json\n"+
		`{"id":"r2","time":"2026-09-13T10:00:00Z","rating":3}`+"\n"+
		`{"id":"r3","word_id":"w2","time":"2026-09-14T10:00:00Z","rating":4}`+"\n")

	got, warnings, err := OpenReviews(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if len(warnings) != 2 {
		t.Errorf("got %d warnings, want 2: %v", len(warnings), warnings)
	}
}

func TestReviewAppendIsOneLinePerEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.jsonl")
	store := OpenReviews(path)
	for i := 0; i < 5; i++ {
		if err := store.Append(review("r", "w", memory.Good, testNow)); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 5 {
		t.Errorf("got %d newline-terminated lines, want 5", lines)
	}
	if !json.Valid([]byte(`{"a":1}`)) {
		t.Fatal("sanity")
	}
}

func TestMemorySaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	states := map[string]memory.State{
		"w1": {Box: 4, NextReview: time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC), LastReview: time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC)},
		"w2": {Box: 1, NextReview: time.Date(2026, time.September, 20, 0, 0, 0, 0, time.UTC)},
	}
	mf := NewMemoryFile(states, "sha256:abc123")
	if err := SaveMemory(path, mf); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != memoryVersion {
		t.Errorf("version = %d, want %d", loaded.Version, memoryVersion)
	}
	if loaded.GeneratedFrom != "sha256:abc123" {
		t.Errorf("generated_from = %q", loaded.GeneratedFrom)
	}

	back, err := loaded.States()
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 {
		t.Fatalf("got %d states, want 2", len(back))
	}
	if !back["w1"].NextReview.Equal(states["w1"].NextReview) || back["w1"].Box != 4 {
		t.Errorf("w1 round-tripped as %+v", back["w1"])
	}
	if !back["w2"].LastReview.IsZero() {
		t.Errorf("w2 should have no last_review, got %v", back["w2"].LastReview)
	}
}

func TestMemoryDatesAreCalendarDates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	// A state carrying a time of day must serialize as a plain date, otherwise
	// two equivalent caches would differ byte for byte.
	states := map[string]memory.State{
		"w1": {Box: 2, NextReview: time.Date(2026, time.October, 15, 17, 42, 3, 0, time.UTC)},
	}
	if err := SaveMemory(path, NewMemoryFile(states, "fp")); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Items map[string]struct {
			NextReview string `json:"next_review"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if got := raw.Items["w1"].NextReview; got != "2026-10-15" {
		t.Errorf("next_review = %q, want %q", got, "2026-10-15")
	}
}

func TestMemoryStaleDetection(t *testing.T) {
	mf := NewMemoryFile(nil, "sha256:one")
	if mf.Stale("sha256:one") {
		t.Error("a cache matching its inputs must not be stale")
	}
	if !mf.Stale("sha256:two") {
		t.Error("a cache built from different inputs must be stale")
	}

	old := &MemoryFile{Version: memoryVersion - 1, GeneratedFrom: "sha256:one"}
	if !old.Stale("sha256:one") {
		t.Error("an older schema version must be stale even if the inputs match")
	}
}

func TestMemoryRejectsBrokenDates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	writeFile(t, path, `{"version":1,"generated_from":"x","items":{"w1":{"box":3,"next_review":"not-a-date"}}}`)

	mf, err := LoadMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mf.States(); err == nil {
		t.Error("a malformed date must be an error, not a silent zero time")
	}
}

func TestMemoryMissingFileIsReportedNotCrashed(t *testing.T) {
	if _, err := LoadMemory(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("expected an error for a missing cache")
	}
}

func TestFingerprintCoversWordsAndReviews(t *testing.T) {
	dir := t.TempDir()
	paths := PathsIn(dir)

	base, err := Fingerprint(paths)
	if err != nil {
		t.Fatal(err)
	}

	if err := OpenReviews(paths.Reviews).Append(review("r1", "w1", memory.Good, testNow)); err != nil {
		t.Fatal(err)
	}
	withReview, err := Fingerprint(paths)
	if err != nil {
		t.Fatal(err)
	}
	if withReview == base {
		t.Error("adding a review must change the fingerprint")
	}

	if err := OpenWords(paths.Words).Rewrite([]Word{{ID: "1", Word: "evil", Normalized: "evil", Source: "cli", CreatedAt: testNow}}); err != nil {
		t.Fatal(err)
	}
	withWord, err := Fingerprint(paths)
	if err != nil {
		t.Fatal(err)
	}
	if withWord == withReview {
		t.Error("changing words.jsonl must change the fingerprint, since it decides which words are eligible")
	}
}
