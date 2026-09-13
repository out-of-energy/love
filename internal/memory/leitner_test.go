package memory

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 9, 30, 0, 0, time.UTC) // 09:30, to prove we truncate
}

func TestStartOfDayDiscardsTimeOfDay(t *testing.T) {
	got := StartOfDay(day(2026, time.September, 14))
	want := time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("StartOfDay = %v, want %v", got, want)
	}
}

// This table is the specification of the algorithm. If a transition changes,
// this test changes with it, deliberately.
func TestAdvanceTransitionTable(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)

	cases := []struct {
		name    string
		fromBox int
		rating  Rating
		wantBox int
	}{
		{"again from box 1 stays at 1", 1, Again, 1},
		{"again from box 5 returns to 1", 5, Again, 1},
		{"again from the last box returns to 1", cfg.Boxes(), Again, 1},

		{"hard never advances", 1, Hard, 1},
		{"hard never advances from the middle", 5, Hard, 5},
		{"hard never demotes from the middle", 5, Hard, 5},

		{"good advances one box", 1, Good, 2},
		{"good advances one box in the middle", 4, Good, 5},
		{"good is capped at the last box", cfg.Boxes(), Good, cfg.Boxes()},

		{"easy skips a box", 1, Easy, 3},
		{"easy skips a box in the middle", 3, Easy, 5},
		{"easy is capped at the last box", cfg.Boxes(), Easy, cfg.Boxes()},
		{"easy near the end clamps rather than overflowing", cfg.Boxes() - 1, Easy, cfg.Boxes()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := State{Box: tc.fromBox, NextReview: now}
			got := cfg.Advance(prev, tc.rating, now)
			if got.Box != tc.wantBox {
				t.Errorf("box = %d, want %d", got.Box, tc.wantBox)
			}
			if got.Box < 1 || got.Box > cfg.Boxes() {
				t.Errorf("box %d is outside 1..%d", got.Box, cfg.Boxes())
			}
		})
	}
}

func TestAdvanceSchedulesFromTodayNotFromTheDueDate(t *testing.T) {
	cfg := DefaultConfig()
	// The word was due a week ago and the learner finally shows up today.
	prev := State{Box: 1, NextReview: day(2026, time.September, 7)}
	now := day(2026, time.September, 14)

	got := cfg.Advance(prev, Good, now)
	want := time.Date(2026, time.September, 17, 0, 0, 0, 0, time.UTC) // today + 3 days (box 2)
	if !got.NextReview.Equal(want) {
		t.Errorf("NextReview = %v, want %v", got.NextReview, want)
	}
	if !got.LastReview.Equal(StartOfDay(now)) {
		t.Errorf("LastReview = %v, want %v", got.LastReview, StartOfDay(now))
	}
}

func TestAdvanceIgnoresInvalidRatings(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)
	prev := State{Box: 3, NextReview: now}

	for _, bad := range []Rating{0, 5, -1} {
		if got := cfg.Advance(prev, bad, now); got != prev {
			t.Errorf("rating %d changed state to %+v, want it untouched", bad, got)
		}
	}
}

func TestNewStateStartsAtBoxOneDueTomorrow(t *testing.T) {
	cfg := DefaultConfig()
	now := day(2026, time.September, 14)
	got := cfg.NewState(now)

	if got.Box != 1 {
		t.Errorf("box = %d, want 1", got.Box)
	}
	want := time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)
	if !got.NextReview.Equal(want) {
		t.Errorf("NextReview = %v, want %v", got.NextReview, want)
	}
}

func TestIntervalClampsToTheAvailableBoxes(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.Interval(1); got != 1 {
		t.Errorf("Interval(1) = %d, want 1", got)
	}
	if got := cfg.Interval(cfg.Boxes()); got != 365 {
		t.Errorf("Interval(last) = %d, want 365", got)
	}
	if got := cfg.Interval(0); got != 1 {
		t.Errorf("Interval(0) should clamp to box 1, got %d", got)
	}
	if got := cfg.Interval(999); got != 365 {
		t.Errorf("Interval(999) should clamp to the last box, got %d", got)
	}
}

func TestStateDue(t *testing.T) {
	now := day(2026, time.September, 14)
	cases := []struct {
		name string
		next time.Time
		want bool
	}{
		{"overdue is due", day(2026, time.September, 10), true},
		{"due today is due", StartOfDay(now), true},
		{"tomorrow is not due", day(2026, time.September, 15), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (State{Box: 1, NextReview: tc.next}).Due(now); got != tc.want {
				t.Errorf("Due = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFakeClock(t *testing.T) {
	clock := NewFakeClock(day(2026, time.September, 14))
	if got := clock.Now(); !got.Equal(StartOfDay(day(2026, time.September, 14))) {
		t.Errorf("Now = %v", got)
	}
	clock.AdvanceDays(3)
	want := time.Date(2026, time.September, 17, 0, 0, 0, 0, time.UTC)
	if got := clock.Now(); !got.Equal(want) {
		t.Errorf("Now after 3 days = %v, want %v", got, want)
	}
}
