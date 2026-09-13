package review

import (
	"strings"
	"testing"
	"time"

	"github.com/out-of-energy/love/internal/ai"
	"github.com/out-of-energy/love/internal/memory"
	"github.com/out-of-energy/love/internal/storage"
)

type grade struct {
	id     string
	rating memory.Rating
}

type recorder struct{ grades []grade }

func testWord(id, word string) storage.Word {
	return storage.Word{
		ID:         id,
		Word:       word,
		Normalized: word,
		IPA:        "/" + word + "/",
		ELI5:       "simple meaning of " + word,
		Chinese:    "中文",
	}
}

func testNow() time.Time {
	return time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC)
}

func testPorts(words []storage.Word, states map[string]memory.State, plan memory.Plan, expansion map[string]ai.Content) (Ports, *recorder) {
	rec := &recorder{}
	cfg := memory.DefaultConfig()

	return Ports{
		Config: cfg,
		Words:  words,
		States: states,
		Plan:   plan,
		Expansion: func(id string) *ai.Content {
			if c, ok := expansion[id]; ok {
				return &c
			}
			return nil
		},
		Grade: func(id string, prev memory.State, rating memory.Rating) (memory.State, error) {
			rec.grades = append(rec.grades, grade{id, rating})
			return cfg.Advance(prev, rating, testNow()), nil
		},
	}, rec
}

func expansionFor(word string) ai.Content {
	return ai.Content{
		Meaning:  "fuller meaning of " + word,
		Examples: []string{"An example with " + word + "."},
		Scene:    "A scene.",
		Dialogue: []ai.Line{{Speaker: "A", Line: "Do you " + word + "?"}},
	}
}

func run(t *testing.T, p Ports, input string) (string, Result) {
	t.Helper()
	var out strings.Builder
	res, err := p.Run(strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	return out.String(), res
}

func TestEmptyPlanSaysThereIsNothingToDo(t *testing.T) {
	p, _ := testPorts(nil, nil, memory.Plan{}, nil)
	out, res := run(t, p, "")

	if !strings.Contains(out, "没有需要复习的词") {
		t.Errorf("output = %q", out)
	}
	if res.Reviewed != 0 || res.New != 0 {
		t.Errorf("result = %+v", res)
	}
}

// The answer must not be on screen while the learner is still trying to recall
// it. Everything else about the session is negotiable; this is the point of it.
func TestTheAnswerAppearsOnlyAfterEnter(t *testing.T) {
	word := testWord("w1", "maintain")
	plan := memory.Plan{Review: []string{"w1"}}
	p, _ := testPorts([]storage.Word{word}, map[string]memory.State{"w1": {Box: 2}}, plan, nil)

	out, _ := run(t, p, "\n3\n")

	prompt := strings.Index(out, "记得它的意思吗")
	answer := strings.Index(out, "simple meaning of maintain")
	if prompt < 0 {
		t.Fatalf("the recall prompt is missing: %q", out)
	}
	if answer < 0 {
		t.Fatalf("the anchor was never revealed: %q", out)
	}
	if answer < prompt {
		t.Error("the answer was printed before the learner was asked to recall it")
	}
}

func TestExpansionStaysFoldedUntilAskedFor(t *testing.T) {
	word := testWord("w1", "maintain")
	plan := memory.Plan{Review: []string{"w1"}}
	states := map[string]memory.State{"w1": {Box: 2}}
	expansion := map[string]ai.Content{"w1": expansionFor("maintain")}

	folded, _ := testPorts([]storage.Word{word}, states, plan, expansion)
	out, _ := run(t, folded, "\n3\n")

	if strings.Contains(out, "fuller meaning of maintain") {
		t.Error("the expansion was shown without being asked for")
	}
	if !strings.Contains(out, "[e] 展开") {
		t.Errorf("the session should offer the expansion: %q", out)
	}

	opened, _ := testPorts([]storage.Word{word}, states, plan, expansion)
	out, _ = run(t, opened, "\ne\n3\n")

	if !strings.Contains(out, "fuller meaning of maintain") {
		t.Errorf("pressing e should reveal the expansion: %q", out)
	}
	if !strings.Contains(out, "An example with maintain.") {
		t.Errorf("the examples are missing: %q", out)
	}
	if !strings.Contains(out, "Do you maintain?") {
		t.Errorf("the dialogue is missing: %q", out)
	}
}

func TestTheExpansionPromptIsHiddenWhenThereIsNone(t *testing.T) {
	word := testWord("w1", "maintain")
	plan := memory.Plan{Review: []string{"w1"}}
	p, _ := testPorts([]storage.Word{word}, map[string]memory.State{"w1": {Box: 2}}, plan, nil)

	out, _ := run(t, p, "\n3\n")
	if strings.Contains(out, "[e] 展开") {
		t.Error("the session offered an expansion that does not exist")
	}
}

func TestRatingIsRecorded(t *testing.T) {
	word := testWord("w1", "maintain")
	plan := memory.Plan{Review: []string{"w1"}}
	p, rec := testPorts([]storage.Word{word}, map[string]memory.State{"w1": {Box: 2}}, plan, nil)

	_, res := run(t, p, "\n3\n")

	if len(rec.grades) != 1 || rec.grades[0].id != "w1" || rec.grades[0].rating != memory.Good {
		t.Errorf("grades = %+v", rec.grades)
	}
	if res.Reviewed != 1 || res.Graded[memory.Good] != 1 {
		t.Errorf("result = %+v", res)
	}
}

func TestEachDigitMapsToItsRating(t *testing.T) {
	word := testWord("w1", "maintain")
	plan := memory.Plan{Review: []string{"w1"}}
	states := map[string]memory.State{"w1": {Box: 2}}

	for input, want := range map[string]memory.Rating{
		"1": memory.Again,
		"2": memory.Hard,
		"3": memory.Good,
		"4": memory.Easy,
	} {
		p, rec := testPorts([]storage.Word{word}, states, plan, nil)
		run(t, p, "\n"+input+"\n")
		if len(rec.grades) != 1 || rec.grades[0].rating != want {
			t.Errorf("input %q produced %+v, want rating %d", input, rec.grades, want)
		}
	}
}

func TestQuittingBeforeRevealingRecordsNothing(t *testing.T) {
	word := testWord("w1", "maintain")
	plan := memory.Plan{Review: []string{"w1"}}
	p, rec := testPorts([]storage.Word{word}, map[string]memory.State{"w1": {Box: 2}}, plan, nil)

	_, res := run(t, p, "q\n")

	if len(rec.grades) != 0 {
		t.Errorf("quitting graded something: %+v", rec.grades)
	}
	if !res.Quit {
		t.Error("the result should record that the session was abandoned")
	}
}

// Leaving early is a supported outcome, so work already done has to survive it.
func TestQuittingMidwayKeepsEarlierWork(t *testing.T) {
	first := testWord("w1", "maintain")
	second := testWord("w2", "fragile")
	plan := memory.Plan{Review: []string{"w1", "w2"}}
	states := map[string]memory.State{"w1": {Box: 2}, "w2": {Box: 2}}
	p, rec := testPorts([]storage.Word{first, second}, states, plan, nil)

	out, res := run(t, p, "\n3\nq\n")

	if len(rec.grades) != 1 || rec.grades[0].id != "w1" {
		t.Errorf("grades = %+v, want only the first word", rec.grades)
	}
	if !res.Quit {
		t.Error("the result should record the quit")
	}
	// The session prints each word before reading, because you cannot decide to
	// quit without seeing what you are quitting on. What matters is that the
	// second word was shown and then left ungraded.
	if !strings.Contains(out, "fragile") {
		t.Error("the second word should have been displayed before the quit was read")
	}
	if strings.Contains(out, "simple meaning of fragile") {
		t.Error("quitting must not reveal the second word's answer")
	}
}

// End of input behaves like a quit rather than an error: everything graded so
// far is already stored, so there is nothing to unwind.
func TestEndOfInputIsAQuit(t *testing.T) {
	word := testWord("w1", "maintain")
	plan := memory.Plan{Review: []string{"w1"}}
	p, rec := testPorts([]storage.Word{word}, map[string]memory.State{"w1": {Box: 2}}, plan, nil)

	_, res := run(t, p, "")

	if len(rec.grades) != 0 {
		t.Errorf("grades = %+v", rec.grades)
	}
	if !res.Quit {
		t.Error("exhausted input should be reported as a quit")
	}
}

func TestUnrecognisedInputRepromptsRatherThanGuessing(t *testing.T) {
	word := testWord("w1", "maintain")
	plan := memory.Plan{Review: []string{"w1"}}
	p, rec := testPorts([]storage.Word{word}, map[string]memory.State{"w1": {Box: 2}}, plan, nil)

	out, _ := run(t, p, "\nnope\n3\n")

	if len(rec.grades) != 1 || rec.grades[0].rating != memory.Good {
		t.Errorf("grades = %+v", rec.grades)
	}
	if !strings.Contains(out, "输入 1–4 评分") {
		t.Errorf("the session should explain the valid input: %q", out)
	}
}

// ---------------------------------------------------------------------------
// New words.
// ---------------------------------------------------------------------------

func TestANewWordShowsItsAnchorWithoutARecallStep(t *testing.T) {
	word := testWord("w1", "fragile")
	plan := memory.Plan{New: []string{"w1"}}
	p, _ := testPorts([]storage.Word{word}, nil, plan, nil)

	out, _ := run(t, p, "\n")

	if strings.Contains(out, "记得它的意思吗") {
		t.Error("there is nothing to recall for a word never seen before")
	}
	if !strings.Contains(out, "simple meaning of fragile") {
		t.Errorf("the anchor should be shown immediately: %q", out)
	}
}

// Enter is the common case and means "I did not know it", which must leave the
// word in the first box rather than advancing it.
func TestEnterOnANewWordMeansAgain(t *testing.T) {
	word := testWord("w1", "fragile")
	plan := memory.Plan{New: []string{"w1"}}
	p, rec := testPorts([]storage.Word{word}, nil, plan, nil)

	_, res := run(t, p, "\n")

	if len(rec.grades) != 1 || rec.grades[0].rating != memory.Again {
		t.Fatalf("grades = %+v, want one Again", rec.grades)
	}
	if res.New != 1 {
		t.Errorf("result = %+v", res)
	}

	// The recorded state must be box 1, due tomorrow — not box 0, which would
	// leave the word unscheduled.
	next := memory.DefaultConfig().Advance(memory.State{}, memory.Again, testNow())
	if next.Box != 1 {
		t.Errorf("a new word rated Again landed in box %d, want 1", next.Box)
	}
}

// A word the learner already knows should not be forced through the early
// boxes: that spends review budget teaching something already known.
func TestANewWordCanBeMarkedAsAlreadyKnown(t *testing.T) {
	word := testWord("w1", "fragile")
	plan := memory.Plan{New: []string{"w1"}}
	states := map[string]memory.State{}

	for input, want := range map[string]memory.Rating{"3": memory.Good, "4": memory.Easy} {
		p, rec := testPorts([]storage.Word{word}, states, plan, nil)
		run(t, p, input+"\n")
		if len(rec.grades) != 1 || rec.grades[0].rating != want {
			t.Errorf("input %q produced %+v, want rating %d", input, rec.grades, want)
		}
	}
}

func TestReviewWordsComeBeforeNewOnes(t *testing.T) {
	reviewWord := testWord("w1", "maintain")
	newWord := testWord("w2", "fragile")
	plan := memory.Plan{Review: []string{"w1"}, New: []string{"w2"}}
	p, rem := testPorts([]storage.Word{reviewWord, newWord}, map[string]memory.State{"w1": {Box: 2}}, plan, nil)

	out, _ := run(t, p, "\n3\n\n")

	if len(rem.grades) != 2 {
		t.Fatalf("grades = %+v", rem.grades)
	}
	if rem.grades[0].id != "w1" {
		t.Error("due reviews must be worked through before new material")
	}
	if strings.Index(out, "maintain") > strings.Index(out, "fragile") {
		t.Error("the printed order does not match the graded order")
	}
}
