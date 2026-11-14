package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mozership/probe/actionrpc"
	"github.com/mozership/probe/mask/markdown"
)

// levels are the levels in the order the summary line counts them.
var levels = []string{actionrpc.NoteCritical, actionrpc.NoteWarning, actionrpc.NoteInfo}

// alerts are the GitHub alerts a note of each level is shown in.
var alerts = map[string]string{
	actionrpc.NoteCritical: "CAUTION",
	actionrpc.NoteWarning:  "WARNING",
	actionrpc.NoteInfo:     "NOTE",
}

// limit is the most a page can hold where it is written, measured as
// GitHub measures it there.
type limit struct {
	max  int
	size func(string) int
	// what names the unit in the line that says notes were left out.
	what string
	// taken is how much of max is taken already, as by what earlier steps
	// wrote to the same summary.
	taken int
}

// bodyLimit is that of the body of a comment or an issue, 65536
// characters, and summaryLimit that of the summary of a step, 1 MiB.
var (
	bodyLimit    = limit{max: 65536, size: utf8.RuneCountInString, what: "characters"}
	summaryLimit = limit{max: 1 << 20, size: func(s string) int { return len(s) }, what: "bytes"}
)

// parseNotes reads with.notes, a list of entries of the notes context.
func parseNotes(v any) ([]actionrpc.Note, error) {
	notes, err := actionrpc.ParseNotes(v)
	if err != nil {
		// The error starts with notes, as notes[1]: body is missing.
		return nil, fmt.Errorf("with.%w", err)
	}
	return notes, nil
}

// page is what the action writes: the marker, body, the line counting the
// notes, and the notes.
type page struct {
	marker string
	body   string
	notes  []actionrpc.Note
	// footer is a line after the notes, such as a link to the run.
	footer string
}

// render writes the page as Markdown within l. When the notes do not all
// fit, the last are left out, and a line says how many. Each part is
// written and measured once, so a long page costs no more than a short one.
// It fails when the page does not fit even without its notes.
func (p page) render(l limit) (string, error) {
	var head strings.Builder
	if p.marker != "" {
		head.WriteString(p.marker + "\n")
	}
	if body := strings.TrimSpace(p.body); body != "" {
		head.WriteString(escapeMasks(body) + "\n\n")
	}
	head.WriteString(countLine(p.notes) + "\n")
	var foot string
	if p.footer != "" {
		foot = "\n" + p.footer + "\n"
	}
	leftOut := func(n int) string {
		if n == 0 {
			return ""
		}
		return fmt.Sprintf("\n%s left out, as GitHub takes at most %d %s here. The run has them all.\n", plural(n, "more note is", "more notes are"), l.max, l.what)
	}

	chunks := p.chunks()
	room := l.max - l.taken - l.size(head.String()) - l.size(foot)
	sizes := make([]int, len(chunks))
	total := 0
	for i, c := range chunks {
		sizes[i] = l.size(c)
		total += sizes[i]
	}
	// Every note, with no line saying some are left out, or the longest
	// run of them from the first that fits beside that line.
	shown := len(chunks)
	if total > room {
		shown = -1
		used := 0
		for k := 0; k < len(chunks); k++ {
			if used+l.size(leftOut(len(chunks)-k)) > room {
				break
			}
			shown = k
			used += sizes[k]
		}
	}
	if shown < 0 {
		if l.taken > 0 {
			return "", fmt.Errorf("the page does not fit in what is left of the %d %s GitHub takes here, %d, even without the notes", l.max, l.what, l.max-l.taken)
		}
		return "", fmt.Errorf("with.body is too long: the page is more than the %d %s GitHub takes here even without the notes", l.max, l.what)
	}

	var b strings.Builder
	b.WriteString(head.String())
	for _, c := range chunks[:shown] {
		b.WriteString(c)
	}
	b.WriteString(leftOut(len(chunks) - shown))
	b.WriteString(foot)
	return b.String(), nil
}

// chunks writes each note, after the name of its job and of its step where
// they change from those of the note before.
func (p page) chunks() []string {
	out := make([]string, 0, len(p.notes))
	var job, step string
	for i, n := range p.notes {
		var b strings.Builder
		if jk := n.JobID + "\x00" + n.Job; i == 0 || jk != job {
			b.WriteString("\n" + escapeMasks("### "+orDash(n.Job)) + "\n")
			job, step = jk, ""
		}
		if key := fmt.Sprintf("%d\x00%s\x00%d", n.Index, n.Step, n.Run); step == "" || key != step {
			b.WriteString("\n" + escapeMasks("**"+orDash(n.Step)+"** · "+stepState(n)) + "\n")
			step = key
		}
		b.WriteString("\n" + alert(n))
		out = append(out, b.String())
	}
	return out
}

// countLine says how many notes there are of each level.
func countLine(notes []actionrpc.Note) string {
	if len(notes) == 0 {
		return "The run left no notes."
	}
	count := map[string]int{}
	for _, n := range notes {
		count[n.Level]++
	}
	var parts []string
	for _, l := range levels {
		if c := count[l]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, l))
		}
	}
	return fmt.Sprintf("The run left %s: %s.", plural(len(notes), "note", "notes"), strings.Join(parts, ", "))
}

// stepState is the status of the step of n, with its run.
func stepState(n actionrpc.Note) string {
	s := orDash(n.Status)
	if n.Run > 0 {
		s += fmt.Sprintf(", run %d", n.Run)
	}
	return s
}

// alert writes n as a GitHub alert of its level.
func alert(n actionrpc.Note) string {
	var b strings.Builder
	fmt.Fprintf(&b, "> [!%s]\n", alerts[n.Level])
	if t := strings.TrimSpace(n.Title); t != "" {
		fmt.Fprintf(&b, "> %s\n>\n", escapeMasks("**"+strings.Join(strings.Fields(t), " ")+"**"))
	}
	for line := range strings.SplitSeq(escapeMasks(strings.TrimRight(n.Body, "\n")), "\n") {
		if line == "" {
			b.WriteString(">\n")
			continue
		}
		b.WriteString("> " + line + "\n")
	}
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// escapeMasks writes the placeholders of the values Probe hides in s,
// <secret:NAME> and <redacted>, as text, which GitHub would take for HTML
// tags and leave out, so that a reader sees where a value was hidden. A name
// is escaped in the line it is written in, as a heading or in bold, rather
// than by itself, where four spaces before it would make it code.
func escapeMasks(s string) string {
	return markdown.Escape(s, markdown.AsEntity)
}
