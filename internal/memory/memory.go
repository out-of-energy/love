// Package memory implements the scheduling core: the Leitner boxes that decide
// when a word comes back, and the daily budget that decides how much is asked
// of the learner.
//
// Nothing in this package reads the clock, the disk, or the network. Time
// arrives through a Clock and everything else is plain data. That is what makes
// a simulated year of learning run in milliseconds, and it is the only reason
// the acceptance tests in the specification can exist at all.
package memory

import "time"

// Rating is the four-level response a learner gives after trying to recall a
// word.
//
// Four levels are stored even though the Leitner interpreter only needs three
// behaviours, because migrating to FSRS later requires the richer signal and
// rewriting learning history is not an option.
type Rating int

const (
	Again Rating = 1 // completely forgotten
	Hard  Rating = 2 // recalled, but with effort
	Good  Rating = 3 // recalled normally
	Easy  Rating = 4 // effortless
)

// Valid reports whether r is one of the four defined ratings.
func (r Rating) Valid() bool { return r >= Again && r <= Easy }

// Config holds every tunable number in the engine.
//
// The defaults are the ones validated by the 365-day simulation in the v0.3
// specification. They are deliberately not obvious: an earlier five-box
// schedule ending at 30 days capped the whole collection at roughly 240 words
// and stalled new-word intake to almost nothing within four months.
type Config struct {
	// Intervals is the number of days spent in each box. Box N uses
	// Intervals[N-1], so the slice length is the number of boxes.
	Intervals []int
	// Capacity is the maximum number of items in one day's session, counting
	// reviews and newly introduced words together.
	Capacity int
	// MaxNew is a hard ceiling on new words per day, applied after reviews
	// have taken their share.
	MaxNew int
}

// DefaultConfig returns the v0.3 validated parameters.
func DefaultConfig() Config {
	return Config{
		Intervals: []int{1, 3, 7, 14, 30, 90, 180, 365},
		Capacity:  16,
		MaxNew:    4,
	}
}

// Boxes reports how many boxes the schedule has.
func (c Config) Boxes() int { return len(c.Intervals) }

// Interval returns the interval in days for a 1-based box number.
func (c Config) Interval(box int) int {
	if box < 1 {
		box = 1
	}
	if box > len(c.Intervals) {
		box = len(c.Intervals)
	}
	return c.Intervals[box-1]
}

// State is one word's derived memory state.
//
// Box is 1-based to match the specification, so a word in box 3 is waiting
// Intervals[2] days. There is exactly one source of truth here: the box
// determines the interval. Nothing else is cached alongside it, because two
// parallel representations of the same fact always drift apart.
type State struct {
	Box        int
	NextReview time.Time
	LastReview time.Time
}

// Due reports whether the word should be reviewed on the given day.
func (s State) Due(now time.Time) bool {
	return !s.NextReview.After(StartOfDay(now))
}

// NewState returns the state a newly introduced word starts in: box 1, first
// review one day later. The word still occupies a slot in today's session as a
// new word; this is the state it carries forward.
func (c Config) NewState(now time.Time) State {
	today := StartOfDay(now)
	return State{Box: 1, NextReview: today.AddDate(0, 0, c.Interval(1))}
}

// StartOfDay truncates t to midnight in its own location.
//
// All scheduling is day-granular: the word file stores dates, the learner
// thinks in dates, and day granularity keeps an interval such as "3 days" from
// depending on the hour the session happened to start.
func StartOfDay(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

// Clock supplies the current time.
//
// The engine never calls time.Now directly. Injecting time is what lets the
// acceptance suite replay a year of learning, a missed month, and a scheduler
// migration deterministically.
type Clock interface {
	Now() time.Time
}

// FakeClock is a Clock that only moves when told to.
type FakeClock struct {
	now time.Time
}

// NewFakeClock returns a clock stopped at the start of t's day.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: StartOfDay(t)}
}

// Now returns the clock's current time.
func (c *FakeClock) Now() time.Time { return c.now }

// AdvanceDays moves the clock forward by whole days.
func (c *FakeClock) AdvanceDays(days int) { c.now = c.now.AddDate(0, 0, days) }
