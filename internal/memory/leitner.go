package memory

import "time"

// Advance applies one rating to a word's state and returns the new state.
//
// This transition table is the entire algorithm, so it is written as an
// explicit switch rather than a formula. Published descriptions of Leitner
// scheduling disagree about what "hard" and "easy" should do; the v0.3
// specification left them as prose ("reduced growth", "faster interval"), which
// is not implementable and, worse, is not testable. The behaviour is therefore
// pinned here:
//
//	Again -> box 1        a lapse returns the word to the start
//	Hard  -> unchanged    recalled, so not a failure; effortful, so not progress
//	Good  -> box + 1      normal progress
//	Easy  -> box + 2      skip a box
//
// Hard deliberately does not demote. The learner did recall the word; punishing
// that would make the second rating indistinguishable from the first and would
// cost the signal that separates "I struggled" from "I failed".
func (c Config) Advance(prev State, rating Rating, now time.Time) State {
	if !rating.Valid() {
		// An unknown rating must never silently corrupt a schedule.
		return prev
	}

	box := prev.Box
	if box < 1 {
		box = 1
	}
	last := c.Boxes()

	switch rating {
	case Again:
		box = 1
	case Hard:
		// unchanged
	case Good:
		if box < last {
			box++
		}
	case Easy:
		box += 2
		if box > last {
			box = last
		}
	}

	today := StartOfDay(now)
	return State{
		Box:        box,
		NextReview: today.AddDate(0, 0, c.Interval(box)),
		LastReview: today,
	}
}
