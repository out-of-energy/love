// Package mail renders the daily digest as an email.
//
// The layout lives in a template, not in the model's output. That division is
// the whole point: the model supplies structured fields, the template decides
// what they look like, so the same content always produces byte-identical HTML
// and a change of wording from the model can never move a border.
//
// The template is embedded rather than read at run time, so the binary stays a
// single file with nothing to install alongside it.
package mail

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/out-of-energy/love/internal/ai"
)

//go:embed templates/review.html
var reviewHTML string

var reviewTemplate = template.Must(template.New("review").Parse(reviewHTML))

// Word is one entry in the digest.
//
// The Chinese gloss is part of this struct even though the terminal never
// prints it: the email is read on a phone, in a queue or on a train, and there
// the gloss is help rather than a spoiler.
type Word struct {
	Word    string
	IPA     string
	Phonics string
	Parts   string
	ELI5    string
	Chinese string

	// Content is the expansion layer, and it is nil when generation failed.
	// The template then renders the anchor alone rather than dropping the word:
	// a broken AI call must never cost the learner today's review.
	Content *ai.Content
}

// Digest is one day's email.
type Digest struct {
	Words []Word
	Date  time.Time
}

// view is what the template actually sees, so the template never does date
// arithmetic or pluralisation of its own.
type view struct {
	Title    string
	DateLine string
	Words    []Word
}

var weekdayNames = [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}

func newView(d Digest) view {
	return view{
		Title:    "每日英语复习",
		DateLine: fmt.Sprintf("%d年%d月%d日 %s", d.Date.Year(), int(d.Date.Month()), d.Date.Day(), weekdayNames[int(d.Date.Weekday())]),
		Words:    d.Words,
	}
}

// Subject returns the subject line for a digest.
func Subject(d Digest) string {
	return fmt.Sprintf("英语复习 · %d 个词 · %d月%d日", len(d.Words), int(d.Date.Month()), d.Date.Day())
}

// RenderHTML returns the HTML body.
//
// html/template escapes every field, which matters here because the values came
// from a model: a stray angle bracket in an example sentence would otherwise
// break the message or worse.
func RenderHTML(d Digest) (string, error) {
	var buf bytes.Buffer
	if err := reviewTemplate.Execute(&buf, newView(d)); err != nil {
		return "", fmt.Errorf("rendering the digest: %w", err)
	}
	return buf.String(), nil
}

// RenderText returns the plain-text alternative.
//
// A text part is not optional politeness: a message without one is more likely
// to be treated as bulk mail, and it is the only version some clients will show.
func RenderText(d Digest) (string, error) {
	var b strings.Builder
	v := newView(d)

	b.WriteString(v.Title)
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s · 共 %d 个词\n", v.DateLine, len(d.Words))

	for _, w := range d.Words {
		b.WriteString("\n")
		b.WriteString(strings.Repeat("-", 32))
		b.WriteString("\n\n")

		fmt.Fprintf(&b, "%s  %s\n", w.Word, w.IPA)
		// The form lines are printed only when the record has them: an
		// upgraded file is honest about what it holds, and love --backfill is
		// what fills the rest.
		if w.Phonics != "" {
			fmt.Fprintf(&b, "Phonics: %s\n", w.Phonics)
		}
		if w.Parts != "" {
			fmt.Fprintf(&b, "Parts: %s\n", w.Parts)
		}
		fmt.Fprintf(&b, "ELI5: %s\n", w.ELI5)
		fmt.Fprintf(&b, "中文：%s\n", w.Chinese)

		if w.Content == nil {
			continue
		}

		b.WriteString("\n扩展\n")
		fmt.Fprintf(&b, "%s\n", w.Content.Meaning)
		if w.Content.Scene != "" {
			fmt.Fprintf(&b, "场景：%s\n", w.Content.Scene)
		}
		if len(w.Content.Examples) > 0 {
			b.WriteString("例句：\n")
			for _, e := range w.Content.Examples {
				fmt.Fprintf(&b, "  · %s\n", e)
			}
		}
		if len(w.Content.Dialogue) > 0 {
			b.WriteString("对话：\n")
			for _, line := range w.Content.Dialogue {
				fmt.Fprintf(&b, "  %s  %s\n", line.Speaker, line.Line)
			}
		}
	}

	b.WriteString("\n回到电脑后运行 love --review 完成今天的复习。\n")
	b.WriteString("邮件只做提醒；评分在本地完成，词库始终在你自己机器上。\n")
	return b.String(), nil
}
