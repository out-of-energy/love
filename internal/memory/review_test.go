package memory

import (
	"testing"
	"time"
)

func at(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 8, 0, 0, 0, time.UTC)
}

func TestNewReviewRecordsTheObservedTransition(t *testing.T) {
	cfg := DefaultConfig()
	prev := State{Box: 3, NextReview: at(2026, time.September, 14)}

	got := cfg.NewReview("review-001", "w1", prev, Good, at(2026, time.September, 14))

	if got.BeforeBox != 3 || got.AfterBox != 4 {
		t.Errorf("box transition recorded as %d -> %d, want 3 -> 4", got.BeforeBox, got.AfterBox)
	}
	if got.OldInterval != 7 || got.NewInterval != 14 {
		t.Errorf("intervals recorded as %d -> %d, want 7 -> 14", got.OldInterval, got.NewInterval)
	}
	if got.Algorithm != AlgorithmLeitner {
		t.Errorf("algorithm = %q, want %q", got.Algorithm, AlgorithmLeitner)
	}
	if got.Rating != Good || got.WordID != "w1" || got.ID != "review-001" {
		t.Errorf("event identity is wrong: %+v", got)
	}
}

func TestNewReviewOnABrandNewWord(t *testing.T) {
	cfg := DefaultConfig()
	// A word that has never been studied has no previous box.
	got := cfg.NewReview("r1", "w1", State{}, Good, at(2026, time.September, 14))

	if got.BeforeBox != 0 {
		t.Errorf("BeforeBox = %d, want 0 for a first review", got.BeforeBox)
	}
	if got.AfterBox != 2 {
		t.Errorf("AfterBox = %d, want 2 (box 1 advanced by Good)", got.AfterBox)
	}
}

func TestReduceReplaysEventsInTimeOrder(t *testing.T) {
	cfg := DefaultConfig()
	// Deliberately shuffled: a merged or restored file may not be sorted.
	events := []Review{
		{ID: "c", WordID: "w1", Time: at(2026, time.September, 20), Rating: Good, BeforeBox: 3},
		{ID: "a", WordID: "w1", Time: at(2026, time.September, 1), Rating: Good, BeforeBox: 1},
		{ID: "b", WordID: "w1", Time: at(2026, time.September, 10), Rating: Easy, BeforeBox: 2},
	}

	got := cfg.Reduce(events)
	if want := 5; got["w1"].Box != want {
		t.Errorf("box = %d, want %d", got["w1"].Box, want)
	}
}

func TestReduceIgnoresTheOrderEventsSitInTheFile(t *testing.T) {
	cfg := DefaultConfig()
	inOrder := []Review{
		{ID: "a", WordID: "w1", Time: at(2026, time.September, 1), Rating: Good, BeforeBox: 1},
		{ID: "b", WordID: "w1", Time: at(2026, time.September, 5), Rating: Again, BeforeBox: 2},
		{ID: "c", WordID: "w1", Time: at(2026, time.September, 9), Rating: Hard, BeforeBox: 1},
	}
	reversed := []Review{inOrder[2], inOrder[1], inOrder[0]}

	first := cfg.Reduce(inOrder)
	second := cfg.Reduce(reversed)

	if first["w1"] != second["w1"] {
		t.Errorf("replay depends on file order: %+v vs %+v", first["w1"], second["w1"])
	}
}

func TestReduceTrustsTheRecordedBeforeBoxForAFirstEvent(t *testing.T) {
	cfg := DefaultConfig()
	// A trimmed log might begin mid-history: the word was already in box 5.
	events := []Review{
		{ID: "a", WordID: "w1", Time: at(2026, time.September, 1), Rating: Hard, BeforeBox: 5},
	}

	got := cfg.Reduce(events)
	if got["w1"].Box != 5 {
		t.Errorf("box = %d, want Hard on box 5 to leave it at 5", got["w1"].Box)
	}
}

func TestReduceHandlesAnEmptyLog(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.Reduce(nil); len(got) != 0 {
		t.Errorf("got %d states from an empty log", len(got))
	}
}

func TestReduceTracksWordsIndependently(t *testing.T) {
	cfg := DefaultConfig()
	events := []Review{
		{ID: "a", WordID: "w1", Time: at(2026, time.September, 1), Rating: Easy, BeforeBox: 1},
		{ID: "b", WordID: "w2", Time: at(2026, time.September, 1), Rating: Again, BeforeBox: 1},
	}
	got := cfg.Reduce(events)

	if got["w1"].Box != 3 {
		t.Errorf("w1 box = %d, want 3", got["w1"].Box)
	}
	if got["w2"].Box != 1 {
		t.Errorf("w2 box = %d, want 1", got["w2"].Box)
	}
}
