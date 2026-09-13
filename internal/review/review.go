// Package review runs one interactive review session.
//
// The session follows the two-layer model: recall first, then the anchor as the
// answer, then optionally the expansion. Part two is folded away by default
// because review should be fast; expanding is a deliberate extra step, not
// something to scroll past on the way to the rating.
package review

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/out-of-energy/love/internal/ai"
	"github.com/out-of-energy/love/internal/memory"
	"github.com/out-of-energy/love/internal/storage"
)

// Ports is everything a session needs from outside itself. Keeping these as
// functions rather than concrete stores is what lets a whole session be driven
// from a string in a test.
type Ports struct {
	Config memory.Config
	Words  []storage.Word
	States map[string]memory.State
	Color  bool

	// Plan is the session to run. It is supplied rather than computed here so
	// that the session has no opinion about what "today" means.
	Plan memory.Plan

	// Expansion returns the cached expansion layer, or nil when there is none.
	// It must never touch the network: a review must not stall on a model
	// before showing material the store already holds.
	Expansion func(wordID string) *ai.Content

	// Grade records one graded review, returning the resulting state.
	//
	// A brand-new word is graded too, with an empty previous state. That is
	// what keeps every state change backed by an event: an introduction that
	// lived only in memory.json would be erased by the next rebuild, and the
	// word would silently return to the unlearned pool.
	Grade func(wordID string, prev memory.State, rating memory.Rating) (memory.State, error)
}

// Result summarises a session.
type Result struct {
	Reviewed int
	New      int
	Graded   map[memory.Rating]int
	Quit     bool
}

// Run drives the session. It returns no error for a normal quit: leaving early
// is a supported outcome, and everything graded before it is already stored.
func (p Ports) Run(in io.Reader, out io.Writer) (Result, error) {
	res := Result{Graded: map[memory.Rating]int{}}

	byID := make(map[string]storage.Word, len(p.Words))
	for _, w := range p.Words {
		byID[w.ID] = w
	}

	total := p.Plan.Total()
	if total == 0 {
		fmt.Fprintln(out, "今天没有需要复习的词。")
		return res, nil
	}

	fmt.Fprintf(out, "今日复习 · %d 个（到期 %d，新词 %d）\n", total, len(p.Plan.Review), len(p.Plan.New))

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	n := 0
	for _, id := range p.Plan.Review {
		w, ok := byID[id]
		if !ok {
			continue
		}
		n++
		quit, err := p.reviewOne(scanner, out, n, total, w, &res)
		if err != nil {
			return res, err
		}
		if quit {
			res.Quit = true
			return res, nil
		}
	}

	for _, id := range p.Plan.New {
		w, ok := byID[id]
		if !ok {
			continue
		}
		n++
		quit, err := p.introduceOne(scanner, out, n, total, w, &res)
		if err != nil {
			return res, err
		}
		if quit {
			res.Quit = true
			return res, nil
		}
	}

	p.summary(out, res)
	return res, nil
}

// readLine returns the next line, and false at end of input. End of input is
// treated exactly like quitting: everything already graded is stored, so there
// is nothing to unwind.
func readLine(scanner *bufio.Scanner) (string, bool) {
	if !scanner.Scan() {
		return "", false
	}
	return strings.TrimSpace(scanner.Text()), true
}

func (p Ports) reviewOne(scanner *bufio.Scanner, out io.Writer, n, total int, w storage.Word, res *Result) (bool, error) {
	fmt.Fprintf(out, "\n%d / %d\n\n  %s\n\n", n, total, w.Word)
	fmt.Fprint(out, "  记得它的意思吗？  [回车揭示 · q 退出]\n> ")

	if line, ok := readLine(scanner); !ok || isQuit(line) {
		return true, nil
	}

	p.revealAnchor(out, w)
	expansion := p.expansionFor(w.ID)
	if expansion != nil {
		fmt.Fprintln(out, "  [e] 展开例句与对话")
	}
	fmt.Fprintln(out, "  评分： 1 忘记   2 模糊   3 记住   4 轻松     [q 退出]")

	for {
		fmt.Fprint(out, "> ")
		line, ok := readLine(scanner)
		if !ok || isQuit(line) {
			return true, nil
		}

		switch strings.ToLower(line) {
		case "e":
			if expansion != nil {
				writeExpansion(out, *expansion)
			} else {
				fmt.Fprintln(out, "  （这个词还没有扩展内容）")
			}
			continue
		case "1", "2", "3", "4":
			rating := memory.Rating(line[0] - '0')
			prev := p.States[w.ID]
			if prev.Box < 1 {
				// A word with no recorded state has never been graded; treat it
				// as sitting in the first box rather than as box zero.
				prev = memory.State{Box: 1}
			}
			if p.Grade != nil {
				if _, err := p.Grade(w.ID, prev, rating); err != nil {
					return false, err
				}
			}
			res.Reviewed++
			res.Graded[rating]++
			return false, nil
		default:
			fmt.Fprintln(out, "  输入 1–4 评分，e 展开，q 退出。")
		}
	}
}

// introduceOne shows a word for the first time and records how well it was
// already known.
//
// Pressing enter is the common case and means "I did not know it", which leaves
// the word in box one. The other two ratings exist so that a word already
// familiar is not forced through the early boxes: that would spend review
// budget teaching the learner something they already know.
func (p Ports) introduceOne(scanner *bufio.Scanner, out io.Writer, n, total int, w storage.Word, res *Result) (bool, error) {
	fmt.Fprintf(out, "\n%d / %d  新词\n\n  %s\n\n", n, total, w.Word)

	p.revealAnchor(out, w)
	if expansion := p.expansionFor(w.ID); expansion != nil {
		writeExpansion(out, *expansion)
	}
	fmt.Fprint(out, "  [回车] 不认识 · [3] 认识 · [4] 很熟 · [q] 退出\n")

	for {
		fmt.Fprint(out, "> ")
		line, ok := readLine(scanner)
		if !ok || isQuit(line) {
			return true, nil
		}

		var rating memory.Rating
		switch strings.ToLower(line) {
		case "":
			rating = memory.Again
		case "3":
			rating = memory.Good
		case "4":
			rating = memory.Easy
		default:
			fmt.Fprintln(out, "  回车表示不认识，或输入 3 / 4。")
			continue
		}

		if p.Grade != nil {
			// An empty previous state is how a first exposure is recorded; the
			// resulting event carries before_box 0, so a replay can tell an
			// introduction apart from a lapse.
			if _, err := p.Grade(w.ID, memory.State{}, rating); err != nil {
				return false, err
			}
		}
		res.New++
		res.Graded[rating]++
		return false, nil
	}
}

func (p Ports) revealAnchor(out io.Writer, w storage.Word) {
	fmt.Fprintln(out)
	writeAnchor(out, w, p.Color)
	fmt.Fprintln(out)
}

func (p Ports) expansionFor(wordID string) *ai.Content {
	if p.Expansion == nil {
		return nil
	}
	return p.Expansion(wordID)
}

func (p Ports) summary(out io.Writer, res Result) {
	if res.Reviewed == 0 && res.New == 0 {
		return
	}
	fmt.Fprintf(out, "\n完成：复习 %d，新词 %d\n", res.Reviewed, res.New)
}

func isQuit(line string) bool {
	switch strings.ToLower(line) {
	case "q", "quit", "exit":
		return true
	}
	return false
}
