package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/out-of-energy/love/internal/memory"
)

// ratingCycle is a deterministic sequence of learner responses, so the replay
// test does not depend on a random source.
var ratingCycle = []memory.Rating{memory.Good, memory.Good, memory.Easy, memory.Hard, memory.Again, memory.Good}

// runDays drives the engine the way the CLI will: plan the day, record what the
// learner answered, and keep an incrementally updated state map.
func runDays(t *testing.T, cfg memory.Config, paths Paths, words []Word, days int, start time.Time) map[string]memory.State {
	t.Helper()

	wordStore := OpenWords(paths.Words)
	reviewStore := OpenReviews(paths.Reviews)

	unlearned := make([]string, 0, len(words))
	for _, w := range words {
		unlearned = append(unlearned, w.ID)
	}

	states := map[string]memory.State{}
	now := start
	counter := 0

	for day := 0; day < days; day++ {
		plan := cfg.PlanDay(now, states, unlearned)

		for i, id := range plan.Review {
			prev := states[id]
			rating := ratingCycle[counter%len(ratingCycle)]
			counter++

			eventID := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, i, 0, time.UTC).Format("20060102-150405")
			reviewStore.Append(cfg.NewReview(eventID, id, prev, rating, now))
			states[id] = cfg.Advance(prev, rating, now)
		}

		for _, id := range plan.New {
			states[id] = cfg.NewState(now)
		}
		unlearned = unlearned[len(plan.New):]

		now = now.AddDate(0, 0, 1)
	}

	if err := wordStore.Rewrite(words); err != nil {
		t.Fatal(err)
	}
	return states
}

func seedWords(t *testing.T, paths Paths, n int) []Word {
	t.Helper()
	store := OpenWords(paths.Words)
	for i := 0; i < n; i++ {
		w := Word{Word: string(rune('a'+i%26)) + string(rune('0'+i/26)), IPA: "/x/", ELI5: "e", Chinese: "中"}
		if _, err := store.Add(w, testNow); err != nil {
			t.Fatal(err)
		}
	}
	words, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return words
}

// The specification requires that state can be recovered from events alone.
// This is the test that makes memory.json safe to delete.
func TestRebuildFromEventsMatchesIncrementalState(t *testing.T) {
	dir := t.TempDir()
	paths := PathsIn(dir)
	cfg := memory.DefaultConfig()

	words := seedWords(t, paths, 24)
	incremental := runDays(t, cfg, paths, words, 120, testNow)

	if err := SaveMemory(paths.Memory, NewMemoryFile(incremental, "placeholder")); err != nil {
		t.Fatal(err)
	}

	// Throw the cache away, exactly as a user might.
	if err := os.Remove(paths.Memory); err != nil {
		t.Fatal(err)
	}

	rebuilt, states, err := Rebuild(cfg, paths)
	if err != nil {
		t.Fatal(err)
	}

	if len(states) != len(incremental) {
		t.Fatalf("rebuilt %d states, incremental had %d", len(states), len(incremental))
	}
	for id, want := range incremental {
		got, ok := states[id]
		if !ok {
			t.Errorf("word %s is missing after replay", id)
			continue
		}
		if got.Box != want.Box {
			t.Errorf("word %s box = %d, want %d", id, got.Box, want.Box)
		}
		if !got.NextReview.Equal(want.NextReview) {
			t.Errorf("word %s next review = %v, want %v", id, got.NextReview, want.NextReview)
		}
	}

	// The rebuilt cache must also be self-consistent: its fingerprint must
	// match the current inputs, or the next run would rebuild again forever.
	current, err := Fingerprint(paths)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Stale(current) {
		t.Error("the rebuilt cache is immediately stale, which would loop")
	}
}

func TestRebuildIsByteForByteReproducible(t *testing.T) {
	dir := t.TempDir()
	paths := PathsIn(dir)
	cfg := memory.DefaultConfig()

	words := seedWords(t, paths, 10)
	runDays(t, cfg, paths, words, 60, testNow)

	first, _, err := Rebuild(cfg, paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveMemory(paths.Memory, first); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(paths.Memory)
	if err != nil {
		t.Fatal(err)
	}

	second, _, err := Rebuild(cfg, paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveMemory(paths.Memory, second); err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(paths.Memory)
	if err != nil {
		t.Fatal(err)
	}

	if string(firstBytes) != string(secondBytes) {
		t.Error("rebuilding twice produced different caches, so replay is not deterministic")
	}
}

func TestRebuildWithNoHistoryProducesAnEmptyCache(t *testing.T) {
	paths := PathsIn(t.TempDir())
	cfg := memory.DefaultConfig()

	mf, states, err := Rebuild(cfg, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 || len(mf.Items) != 0 {
		t.Errorf("expected an empty cache, got %d states", len(states))
	}
}

func TestMemoryPathIsCreatedUnderTheStoreDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	paths := PathsIn(dir)
	cfg := memory.DefaultConfig()

	words := seedWords(t, paths, 3)
	states := runDays(t, cfg, paths, words, 5, testNow)
	if err := SaveMemory(paths.Memory, NewMemoryFile(states, "x")); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(paths.Memory); err != nil {
		t.Fatalf("cache was not written: %v", err)
	}
}
