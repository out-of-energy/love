package memory

import (
	"sort"
	"time"
)

// AlgorithmLeitner names the scheduler recorded on every event. Storing the
// algorithm per event is what makes a later move to SM-2 or FSRS auditable:
// old events keep saying what actually produced them.
const AlgorithmLeitner = "leitner"

// Review is one recorded learning event.
//
// It carries both the cause (the rating) and the observed effect (the boxes and
// intervals). Storing the effect looks redundant against pure event sourcing,
// but it lets the history be audited and lets a future algorithm be evaluated
// against what the old one really did, rather than against a re-derivation that
// silently changes when the algorithm does.
type Review struct {
	ID          string    `json:"id"`
	WordID      string    `json:"word_id"`
	Time        time.Time `json:"time"`
	Rating      Rating    `json:"rating"`
	Algorithm   string    `json:"algorithm"`
	BeforeBox   int       `json:"before_box"`
	AfterBox    int       `json:"after_box"`
	OldInterval int       `json:"old_interval"`
	NewInterval int       `json:"new_interval"`
}

// NewReview builds the event describing what applying rating to prev did.
func (c Config) NewReview(eventID, wordID string, prev State, rating Rating, at time.Time) Review {
	next := c.Advance(prev, rating, at)
	oldInterval := 0
	if prev.Box >= 1 {
		oldInterval = c.Interval(prev.Box)
	}
	return Review{
		ID:          eventID,
		WordID:      wordID,
		Time:        at,
		Rating:      rating,
		Algorithm:   AlgorithmLeitner,
		BeforeBox:   prev.Box,
		AfterBox:    next.Box,
		OldInterval: oldInterval,
		NewInterval: c.Interval(next.Box),
	}
}

// Reduce replays events into the current state of every word.
//
// This function, not memory.json, is the definition of truth. The cache exists
// only to avoid replaying the log on every run; if it is missing, stale or
// hand-edited, replaying restores exactly the same answer. That is the whole
// reason the cache is allowed to be a plain derived file.
//
// Events are sorted by time before replay, so the result does not depend on the
// order lines happen to sit in the file. That matters after a manual merge, a
// restored backup, or any concatenation of two histories.
func (c Config) Reduce(reviews []Review) map[string]State {
	ordered := make([]Review, len(reviews))
	copy(ordered, reviews)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].Time.Equal(ordered[j].Time) {
			return ordered[i].Time.Before(ordered[j].Time)
		}
		return ordered[i].ID < ordered[j].ID
	})

	states := make(map[string]State, len(ordered))
	for _, r := range ordered {
		prev, known := states[r.WordID]
		if !known {
			// A word's first recorded event may start mid-history if the log
			// was trimmed or restored. Trust the recorded before-box over an
			// assumption about where the word began.
			prev = State{Box: r.BeforeBox}
			if prev.Box < 1 {
				prev = State{Box: 1}
			}
		}
		states[r.WordID] = c.Advance(prev, r.Rating, r.Time)
	}
	return states
}
