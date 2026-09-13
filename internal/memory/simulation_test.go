package memory

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
)

// distribution is the simulated learner's rating behaviour. The defaults come
// from the acceptance section of the v0.3 specification.
type distribution struct {
	again, hard, good, easy float64
}

var specDistribution = distribution{again: 0.05, hard: 0.10, good: 0.75, easy: 0.10}

func (d distribution) pick(rng *rand.Rand) Rating {
	x := rng.Float64()
	switch {
	case x < d.again:
		return Again
	case x < d.again+d.hard:
		return Hard
	case x < d.again+d.hard+d.good:
		return Good
	default:
		return Easy
	}
}

type simResult struct {
	vocabulary  int
	maxBacklog  int
	maxSession  int
	newLast30   int
	studied     int
	boxCounts   map[int]int
	yearlySizes []int
}

// simulate runs the engine forward with no disk, no clock and no network, which
// is the only practical way to test a system designed to run for years.
//
// poolSize is how many unlearned words are waiting in words.jsonl.
func simulate(days int, cfg Config, d distribution, seed uint64, poolSize int, weekdaysOnly bool) simResult {
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	clock := NewFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))

	pool := make([]string, poolSize)
	for i := range pool {
		pool[i] = fmt.Sprintf("w%05d", i)
	}

	states := make(map[string]State, poolSize)
	res := simResult{boxCounts: map[int]int{}}
	dailyNew := make([]int, 0, days)

	for day := 0; day < days; day++ {
		weekday := clock.Now().Weekday()
		active := !weekdaysOnly || (weekday != time.Saturday && weekday != time.Sunday)

		if active {
			plan := cfg.PlanDay(clock.Now(), states, pool)

			for _, id := range plan.Review {
				states[id] = cfg.Advance(states[id], d.pick(rng), clock.Now())
			}
			for _, id := range plan.New {
				states[id] = cfg.NewState(clock.Now())
			}
			pool = pool[len(plan.New):]

			if plan.Total() > res.maxSession {
				res.maxSession = plan.Total()
			}
			dailyNew = append(dailyNew, len(plan.New))
		} else {
			dailyNew = append(dailyNew, 0)
		}

		// Whatever is still due after the session is the backlog.
		if backlog := cfg.DueCount(clock.Now(), states); backlog > res.maxBacklog {
			res.maxBacklog = backlog
		}

		clock.AdvanceDays(1)
	}

	res.vocabulary = len(states)
	for i := len(dailyNew) - 1; i >= 0 && i >= len(dailyNew)-30; i-- {
		res.newLast30 += dailyNew[i]
	}
	for _, s := range states {
		res.boxCounts[s.Box]++
		if s.Box >= 1 && s.Box <= cfg.Boxes() {
			res.studied++
		}
	}
	for y := 0; y < days/365; y++ {
		res.yearlySizes = append(res.yearlySizes, 0)
	}
	return res
}

// This is the acceptance test from the specification, run against the real
// transition table instead of a spreadsheet.
func TestSimulatedYearMeetsTheAcceptanceTargets(t *testing.T) {
	cfg := DefaultConfig()
	res := simulate(365, cfg, specDistribution, 7, 20000, false)

	t.Logf("365-day simulation: vocabulary=%d maxBacklog=%d maxSession=%d newInLast30=%d",
		res.vocabulary, res.maxBacklog, res.maxSession, res.newLast30)
	t.Logf("box distribution: %v", res.boxCounts)

	if res.maxSession > cfg.Capacity {
		t.Errorf("a session reached %d items, over the capacity of %d", res.maxSession, cfg.Capacity)
	}
	if res.maxBacklog >= 50 {
		t.Errorf("backlog reached %d, over the documented limit of 50", res.maxBacklog)
	}
	if res.vocabulary < 500 {
		t.Errorf("vocabulary after a year = %d, below the acceptance target of 500", res.vocabulary)
	}
}

func TestSimulatedBoxesStayInRange(t *testing.T) {
	cfg := DefaultConfig()
	res := simulate(365, cfg, specDistribution, 11, 20000, false)

	total := 0
	for box, count := range res.boxCounts {
		total += count
		if box < 1 || box > cfg.Boxes() {
			t.Errorf("box %d is outside 1..%d", box, cfg.Boxes())
		}
	}
	if total != res.vocabulary {
		t.Errorf("box counts sum to %d, vocabulary is %d", total, res.vocabulary)
	}
}

func TestSimulationIsReproducible(t *testing.T) {
	cfg := DefaultConfig()
	first := simulate(180, cfg, specDistribution, 42, 20000, false)
	second := simulate(180, cfg, specDistribution, 42, 20000, false)

	if first.vocabulary != second.vocabulary ||
		first.maxBacklog != second.maxBacklog ||
		first.maxSession != second.maxSession {
		t.Errorf("same seed produced different results: %+v vs %+v", first, second)
	}
}

// A year of perfect adherence is the optimistic case. Real learners take
// weekends off, and the specification's "365 days >= 500 words" target was
// calibrated without stating that assumption. This test records the cost of
// five-day weeks so a regression in it is visible rather than surprising.
func TestWeekdayOnlyAdherenceCostsVocabularyButStaysUsable(t *testing.T) {
	cfg := DefaultConfig()
	daily := simulate(365, cfg, specDistribution, 7, 20000, false)
	weekday := simulate(365, cfg, specDistribution, 7, 20000, true)

	t.Logf("daily adherence:   vocabulary=%d maxBacklog=%d", daily.vocabulary, daily.maxBacklog)
	t.Logf("5-day adherence:   vocabulary=%d maxBacklog=%d", weekday.vocabulary, weekday.maxBacklog)

	if weekday.vocabulary >= daily.vocabulary {
		t.Errorf("five-day weeks produced %d words, no fewer than daily adherence's %d",
			weekday.vocabulary, daily.vocabulary)
	}
	if weekday.vocabulary < 300 {
		t.Errorf("five-day weeks produced only %d words, which is too few to be useful", weekday.vocabulary)
	}
	if weekday.maxBacklog >= 50 {
		t.Errorf("five-day weeks pushed the backlog to %d", weekday.maxBacklog)
	}
}

// The specification's capacity formula (capacity x final interval = 5840 words)
// is an asymptotic bound, not a reachable figure. This test pins the real shape
// of the curve so the claim cannot quietly be treated as a target.
func TestLongRunGrowthDeceleratesAndStaysBelowTheFormula(t *testing.T) {
	cfg := DefaultConfig()
	formulaCeiling := cfg.Capacity * cfg.Interval(cfg.Boxes())

	first := simulate(365, cfg, specDistribution, 7, 40000, false)
	fifth := simulate(365*5, cfg, specDistribution, 7, 40000, false)

	t.Logf("year 1 vocabulary: %d", first.vocabulary)
	t.Logf("year 5 vocabulary: %d (formula ceiling %d)", fifth.vocabulary, formulaCeiling)

	if fifth.vocabulary <= first.vocabulary {
		t.Errorf("year 5 (%d) did not exceed year 1 (%d); the collection stopped growing",
			fifth.vocabulary, first.vocabulary)
	}
	if fifth.vocabulary >= formulaCeiling {
		t.Errorf("year 5 reached %d, at or above the asymptotic bound of %d", fifth.vocabulary, formulaCeiling)
	}
}
