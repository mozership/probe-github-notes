package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Levels of a note, as Probe gives them in the notes context.
const (
	levelInfo     = "info"
	levelWarning  = "warning"
	levelCritical = "critical"
)

// levels are the levels in the order the summary line counts them.
var levels = []string{levelCritical, levelWarning, levelInfo}

// alerts are the GitHub alerts a note of each level is shown in.
var alerts = map[string]string{
	levelCritical: "CAUTION",
	levelWarning:  "WARNING",
	levelInfo:     "NOTE",
}

// maxBody is the most characters GitHub takes in the body of a comment or
// an issue.
const maxBody = 65536

// note is an entry of the notes context of Probe.
type note struct {
	job, jobID, step string
	index            int
	status           string
	level            string
	title, body      string
	// run is the run of a job with repeat the note is of, or 0.
	run int
}

// parseNotes reads with.notes, a list of entries of the notes context.
func parseNotes(v any) ([]note, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf(`with.notes must be a list of notes, such as "{{ notes }}", not %T`, v)
	}
	out := make([]note, 0, len(list))
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("with.notes[%d] must be an entry of notes, not %T", i, e)
		}
		n, err := parseNote(m)
		if err != nil {
			return nil, fmt.Errorf("with.notes[%d]: %w", i, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func parseNote(m map[string]any) (note, error) {
	var n note
	str := func(key string, required bool) (string, error) {
		v, ok := m[key]
		if !ok || v == nil {
			if required {
				return "", fmt.Errorf("%s is missing", key)
			}
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%s must be a string, not %T", key, v)
		}
		return s, nil
	}
	var err error
	if n.level, err = str("level", true); err != nil {
		return n, err
	}
	if _, ok := alerts[n.level]; !ok {
		return n, fmt.Errorf("level %q is not one of info, warning or critical", n.level)
	}
	if n.body, err = str("body", true); err != nil {
		return n, err
	}
	if n.title, err = str("title", false); err != nil {
		return n, err
	}
	if n.job, err = str("job", false); err != nil {
		return n, err
	}
	if n.jobID, err = str("job_id", false); err != nil {
		return n, err
	}
	if n.step, err = str("step", false); err != nil {
		return n, err
	}
	if n.status, err = str("status", false); err != nil {
		return n, err
	}
	if n.index, err = number(m, "index"); err != nil {
		return n, err
	}
	if n.run, err = number(m, "run"); err != nil {
		return n, err
	}
	return n, nil
}

// number reads a whole number of m, 0 when it is absent.
func number(m map[string]any, key string) (int, error) {
	switch v := m[key].(type) {
	case nil:
		return 0, nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v == float64(int(v)) {
			return int(v), nil
		}
	}
	return 0, fmt.Errorf("%s must be a whole number, not %v", key, m[key])
}

// page is what the action writes: the marker, body, the line counting the
// notes, and the notes.
type page struct {
	marker string
	body   string
	notes  []note
	// footer is a line after the notes, such as a link to the run.
	footer string
}

// render writes the page as Markdown. When it is longer than GitHub takes,
// the last notes are left out, and a line says how many.
func (p page) render() string {
	for shown := len(p.notes); ; shown-- {
		s := p.renderWith(shown)
		if len(s) <= maxBody || shown == 0 {
			return s
		}
	}
}

func (p page) renderWith(shown int) string {
	var b strings.Builder
	if p.marker != "" {
		b.WriteString(p.marker + "\n")
	}
	if body := strings.TrimSpace(p.body); body != "" {
		b.WriteString(escapeMasks(body) + "\n\n")
	}
	b.WriteString(countLine(p.notes) + "\n")

	var job, step string
	first := true
	for _, n := range p.notes[:shown] {
		if jk := n.jobID + "\x00" + n.job; first || jk != job {
			fmt.Fprintf(&b, "\n### %s\n", orDash(n.job))
			job, step = jk, ""
		}
		key := fmt.Sprintf("%d\x00%s\x00%d", n.index, n.step, n.run)
		if first || key != step {
			fmt.Fprintf(&b, "\n**%s** · %s\n", orDash(n.step), stepState(n))
			step = key
		}
		first = false
		b.WriteString("\n" + alert(n))
	}
	if left := len(p.notes) - shown; left > 0 {
		fmt.Fprintf(&b, "\n%s left out, as GitHub takes at most %d characters here. The run has them all.\n", plural(left, "more note is", "more notes are"), maxBody)
	}
	if p.footer != "" {
		b.WriteString("\n" + p.footer + "\n")
	}
	return b.String()
}

// countLine says how many notes there are of each level.
func countLine(notes []note) string {
	if len(notes) == 0 {
		return "The run left no notes."
	}
	count := map[string]int{}
	for _, n := range notes {
		count[n.level]++
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
func stepState(n note) string {
	s := orDash(n.status)
	if n.run > 0 {
		s += fmt.Sprintf(", run %d", n.run)
	}
	return s
}

// alert writes n as a GitHub alert of its level.
func alert(n note) string {
	var b strings.Builder
	fmt.Fprintf(&b, "> [!%s]\n", alerts[n.level])
	if t := strings.TrimSpace(n.title); t != "" {
		fmt.Fprintf(&b, "> **%s**\n>\n", escapeMasks(strings.Join(strings.Fields(t), " ")))
	}
	for line := range strings.SplitSeq(escapeMasks(strings.TrimRight(n.body, "\n")), "\n") {
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

// masks are what Probe writes in place of a value it hides: <secret:NAME>
// and <redacted>.
var masks = regexp.MustCompile(`<(secret:[A-Za-z_][A-Za-z0-9_]*|redacted)>`)

// fence opens or closes a fenced code block.
var fence = regexp.MustCompile("^ {0,3}(```|~~~)")

// escapeMasks writes the masks in s as text, which GitHub would take for
// HTML tags and leave out, so that a reader sees where a value was hidden.
// In a code block or a code span a mask is shown as it is, so it is left
// alone there.
func escapeMasks(s string) string {
	lines := strings.Split(s, "\n")
	var open string
	for i, line := range lines {
		if m := fence.FindStringSubmatch(line); m != nil {
			switch {
			case open == "":
				open = m[1]
			case open == m[1]:
				open = ""
			}
			continue
		}
		if open != "" {
			continue
		}
		lines[i] = escapeOutsideSpans(line)
	}
	return strings.Join(lines, "\n")
}

// escapeOutsideSpans escapes the masks of line outside its code spans.
func escapeOutsideSpans(line string) string {
	var b strings.Builder
	for line != "" {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			b.WriteString(escape(line))
			break
		}
		b.WriteString(escape(line[:start]))
		run := len(line[start:]) - len(strings.TrimLeft(line[start:], "`"))
		ticks := line[start : start+run]
		end := strings.Index(line[start+run:], ticks)
		if end < 0 {
			b.WriteString(escape(line[start:]))
			break
		}
		span := start + run + end + run
		b.WriteString(line[start:span])
		line = line[span:]
	}
	return b.String()
}

func escape(s string) string {
	return masks.ReplaceAllString(s, "&lt;$1&gt;")
}
