package memory

import (
	"sort"
	"time"
)

// Plan is one day's study session.
type Plan struct {
	// Review holds word IDs that are due, most overdue first.
	Review []string
	// New holds word IDs being introduced today.
	New []string
}

// Total is the number of items in the session, which never exceeds Capacity.
func (p Plan) Total() int { return len(p.Review) + len(p.New) }

// PlanDay decides what today's session contains.
//
// Reviews are chosen first and consume the budget; new words only get what is
// left. This ordering is the whole point of the cognitive budget. A backlog
// must never be answered by piling on more new material, and a heavy review day
// must be allowed to reduce new-word intake to zero on its own.
//
// Both orderings are fixed, because the alternative is a scheduler whose
// behaviour depends on Go's map iteration order:
//
//	due reviews: NextReview ascending, then box ascending, then ID
//	new words:   the caller's order, i.e. created_at ascending
//
// unlearned is the list of word IDs that have never been studied, in
// words.jsonl order. It is not modified.
func (c Config) PlanDay(now time.Time, states map[string]State, unlearned []string) Plan {
	today := StartOfDay(now)

	type due struct {
		id   string
		next time.Time
		box  int
	}
	dues := make([]due, 0, len(states))
	for id, state := range states {
		if state.Due(today) {
			dues = append(dues, due{id: id, next: state.NextReview, box: state.Box})
		}
	}
	sort.Slice(dues, func(i, j int) bool {
		if !dues[i].next.Equal(dues[j].next) {
			return dues[i].next.Before(dues[j].next)
		}
		if dues[i].box != dues[j].box {
			return dues[i].box < dues[j].box
		}
		// A total order makes replay and simulation reproducible even though
		// the input map has no defined iteration order.
		return dues[i].id < dues[j].id
	})

	plan := Plan{}
	for _, d := range dues {
		if len(plan.Review) >= c.Capacity {
			break
		}
		plan.Review = append(plan.Review, d.id)
	}

	remaining := c.Capacity - len(plan.Review)
	limit := c.MaxNew
	if remaining < limit {
		limit = remaining
	}
	if limit > len(unlearned) {
		limit = len(unlearned)
	}
	if limit > 0 {
		plan.New = append(plan.New, unlearned[:limit]...)
	}
	return plan
}

// DueCount reports how many words are due, including those beyond today's
// capacity. The difference between this and len(plan.Review) is the backlog,
// which is what the stagnation and saturation warnings are built from.
func (c Config) DueCount(now time.Time, states map[string]State) int {
	today := StartOfDay(now)
	count := 0
	for _, state := range states {
		if state.Due(today) {
			count++
		}
	}
	return count
}
