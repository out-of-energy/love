package memory

import (
	"reflect"
	"testing"
	"time"
)

func statesFrom(pairs map[string]int) map[string]State {
	out := make(map[string]State, len(pairs))
	for id, box := range pairs {
		out[id] = State{Box: box, NextReview: time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)}
	}
	return out
}

func TestPlanDayPutsReviewsBeforeNewWords(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	// Sixteen words due: the whole budget. No new words may be introduced.
	due := map[string]int{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"} {
		due[id] = 1
	}
	plan := cfg.PlanDay(now, statesFrom(due), []string{"new1", "new2", "new3"})

	if len(plan.Review) != cfg.Capacity {
		t.Fatalf("reviews = %d, want %d", len(plan.Review), cfg.Capacity)
	}
	if len(plan.New) != 0 {
		t.Errorf("new words = %v, want none: reviews must consume the budget first", plan.New)
	}
}

func TestPlanDayFillsLeftoverCapacityWithNewWords(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	plan := cfg.PlanDay(now, statesFrom(map[string]int{"a": 2}), []string{"n1", "n2", "n3", "n4", "n5"})

	if len(plan.Review) != 1 {
		t.Fatalf("reviews = %d, want 1", len(plan.Review))
	}
	if len(plan.New) != cfg.MaxNew {
		t.Errorf("new words = %d, want %d (the MaxNew cap binds before capacity does)", len(plan.New), cfg.MaxNew)
	}
	if plan.Total() > cfg.Capacity {
		t.Errorf("session of %d exceeds capacity %d", plan.Total(), cfg.Capacity)
	}
}

func TestPlanDayNeverExceedsCapacity(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	// Forty words due, which is far past capacity.
	due := map[string]int{}
	for i := 0; i < 40; i++ {
		due[string(rune('a'+i%26))+string(rune('0'+i/26))] = 1
	}
	plan := cfg.PlanDay(now, statesFrom(due), []string{"n1", "n2", "n3", "n4"})

	if plan.Total() > cfg.Capacity {
		t.Errorf("session of %d exceeds capacity %d", plan.Total(), cfg.Capacity)
	}
}

func TestPlanDayOrdersByMostOverdueThenLowestBox(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	states := map[string]State{
		"recent-low":  {Box: 1, NextReview: day(2026, time.September, 13)},
		"oldest":      {Box: 6, NextReview: day(2026, time.September, 1)},
		"old-high":    {Box: 7, NextReview: day(2026, time.September, 1)},
		"tomorrow":    {Box: 1, NextReview: day(2026, time.September, 15)},
		"future":      {Box: 2, NextReview: day(2026, time.September, 20)},
		"recent-high": {Box: 4, NextReview: day(2026, time.September, 13)},
	}

	plan := cfg.PlanDay(now, states, nil)

	// Not-due words must be excluded entirely.
	want := []string{"oldest", "old-high", "recent-low", "recent-high"}
	if !reflect.DeepEqual(plan.Review, want) {
		t.Errorf("review order = %v, want %v", plan.Review, want)
	}
}

func TestPlanDayIsDeterministicDespiteMapIteration(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	states := map[string]State{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r"} {
		states[id] = State{Box: 1, NextReview: day(2026, time.September, 14)}
	}
	unlearned := []string{"n1", "n2", "n3", "n4", "n5", "n6"}

	first := cfg.PlanDay(now, states, unlearned)
	for i := 0; i < 50; i++ {
		got := cfg.PlanDay(now, states, unlearned)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d produced %v, first run produced %v", i, got, first)
		}
	}
}

func TestPlanDayKeepsCreatedAtOrderForNewWords(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	// words.jsonl order is created_at ascending, so the caller's order is the
	// learning order.
	plan := cfg.PlanDay(now, nil, []string{"first", "second", "third", "fourth", "fifth"})
	want := []string{"first", "second", "third", "fourth"}
	if !reflect.DeepEqual(plan.New, want) {
		t.Errorf("new words = %v, want %v", plan.New, want)
	}
}

func TestPlanDayHandlesEmptyInputs(t *testing.T) {
	cfg := DefaultConfig()
	plan := cfg.PlanDay(day(2026, time.September, 14), nil, nil)
	if plan.Total() != 0 {
		t.Errorf("empty engine produced a session of %d", plan.Total())
	}
}

func TestDueCountIncludesBacklogBeyondCapacity(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	due := map[string]int{}
	for i := 0; i < 30; i++ {
		due[string(rune('a'+i))] = 1
	}
	states := statesFrom(due)

	if got := cfg.DueCount(now, states); got != 30 {
		t.Errorf("DueCount = %d, want 30", got)
	}
	plan := cfg.PlanDay(now, states, nil)
	if backlog := 30 - len(plan.Review); backlog != 30-cfg.Capacity {
		t.Errorf("backlog = %d, want %d", backlog, 30-cfg.Capacity)
	}
}
