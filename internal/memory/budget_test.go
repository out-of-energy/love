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
	cfg.Capacity = 6 // room for both, so "who goes first" is what is under test
	now := day(2026, time.September, 14)

	// Ten words due, of which the day can take all but the floor.
	due := map[string]int{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		due[id] = 1
	}
	plan := cfg.PlanDay(now, statesFrom(due), []string{"new1", "new2", "new3"})

	if len(plan.Review) != cfg.Capacity-cfg.MinNew {
		t.Fatalf("reviews = %d, want %d", len(plan.Review), cfg.Capacity-cfg.MinNew)
	}
	if len(plan.New) != cfg.MinNew {
		t.Errorf("new words = %v, want %d: the floor is held back before reviews are served", plan.New, cfg.MinNew)
	}
}

// The floor is a promise about intake, not a hole in the day: once there is
// nothing left to introduce it must be released, or a finished word list would
// waste capacity on every review-heavy day forever.
func TestPlanDayReleasesTheFloorWhenNothingIsLeftToIntroduce(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	due := map[string]int{}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		due[id] = 1
	}
	plan := cfg.PlanDay(now, statesFrom(due), nil)

	if len(plan.Review) != cfg.Capacity {
		t.Errorf("reviews = %d, want the whole capacity of %d", len(plan.Review), cfg.Capacity)
	}
	if len(plan.New) != 0 {
		t.Errorf("new words = %v, want none", plan.New)
	}
}

// A floor larger than the day cannot be honoured, and the day wins: the session
// stays within capacity and the reviews give up everything they have.
func TestPlanDayKeepsTheFloorInsideCapacity(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Capacity = 1
	cfg.MinNew = 3
	now := day(2026, time.September, 14)

	plan := cfg.PlanDay(now, statesFrom(map[string]int{"due": 1}), []string{"n1", "n2"})

	if plan.Total() != 1 {
		t.Errorf("session = %d items (%v + %v), want 1", plan.Total(), plan.Review, plan.New)
	}
	if len(plan.New) != 1 {
		t.Errorf("new words = %v, want the single slot", plan.New)
	}
}

// A ceiling below the floor is not a real ceiling: the floor is the stronger
// promise, so it wins and the day still gets its new word.
func TestPlanDayHonoursTheFloorOverASmallerCeiling(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxNew = 0
	now := day(2026, time.September, 14)

	plan := cfg.PlanDay(now, statesFrom(map[string]int{"due": 1}), []string{"n1", "n2"})

	if len(plan.New) != cfg.MinNew {
		t.Errorf("new words = %v, want %d despite MaxNew = 0", plan.New, cfg.MinNew)
	}
	if plan.Total() > cfg.Capacity {
		t.Errorf("session of %d exceeds capacity %d", plan.Total(), cfg.Capacity)
	}
}

// MaxNew still binds whenever the day has more room than it allows. The default
// budget no longer exercises that (capacity and ceiling are both two), so the
// test raises the capacity deliberately.
func TestPlanDayFillsLeftoverCapacityWithNewWords(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Capacity = 6
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

// The whole day is filled when reviews leave room for it: a session shorter
// than capacity on a day that had material for it is capacity thrown away.
func TestPlanDayFillsTheDayWhenReviewsAreFew(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	plan := cfg.PlanDay(now, statesFrom(map[string]int{"a": 2}), []string{"n1", "n2", "n3", "n4", "n5"})

	if plan.Total() != cfg.Capacity {
		t.Errorf("session = %d items (%d reviews + %d new), want the full capacity of %d",
			plan.Total(), len(plan.Review), len(plan.New), cfg.Capacity)
	}
}

// The budget is a decision, not an accident of the simulation that produced the
// intervals, so it is pinned here as one.
func TestDefaultBudgetIsTheOneTheLearnerAskedFor(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Capacity != 2 || cfg.MaxNew != 2 || cfg.MinNew != 1 {
		t.Errorf("default budget = capacity %d, ceiling %d, floor %d; want 2 / 2 / 1",
			cfg.Capacity, cfg.MaxNew, cfg.MinNew)
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

	cfg.Capacity = 4 // the ordering is what is under test, so give it room
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
	// learning order. Only the ceiling's worth is taken, and from the front.
	plan := cfg.PlanDay(now, nil, []string{"first", "second", "third", "fourth", "fifth"})
	want := []string{"first", "second"}
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
