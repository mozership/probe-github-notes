package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
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
		if jk := n.jobID + "\x00" + n.job; i == 0 || jk != job {
			fmt.Fprintf(&b, "\n### %s\n", orDash(n.job))
			job, step = jk, ""
		}
		if key := fmt.Sprintf("%d\x00%s\x00%d", n.index, n.step, n.run); step == "" || key != step {
			fmt.Fprintf(&b, "\n**%s** · %s\n", orDash(n.step), stepState(n))
			step = key
		}
		b.WriteString("\n" + alert(n))
		out = append(out, b.String())
	}
	return out
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

// fence opens or closes a fenced code block: three or more backticks or
// tildes, indented by at most three spaces, and what follows them.
var fence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// escapeMasks writes the masks in s as text, which GitHub would take for
// HTML tags and leave out, so that a reader sees where a value was hidden.
// In a code block or a code span a mask is shown as it is, so it is left
// alone there.
func escapeMasks(s string) string {
	lines := strings.Split(s, "\n")
	// open is the fence of the code block the line is in, or empty.
	var open string
	for i, line := range lines {
		if m := fence.FindStringSubmatch(line); m != nil {
			switch {
			case open == "" && !(m[1][0] == '`' && strings.Contains(m[2], "`")):
				open = m[1]
				continue
			case open != "" && m[1][0] == open[0] && len(m[1]) >= len(open) && strings.TrimSpace(m[2]) == "":
				open = ""
				continue
			}
		}
		if open != "" {
			continue
		}
		lines[i] = escapeOutsideSpans(line)
	}
	return strings.Join(lines, "\n")
}

// escapeOutsideSpans escapes the masks of line outside its code spans. A
// code span opens with a run of backticks and closes with the next run of
// the same length; a run with no such closer is text.
func escapeOutsideSpans(line string) string {
	var b strings.Builder
	text := 0
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		n := ticks(line, i)
		end := closer(line, i+n, n)
		if end < 0 {
			i += n
			continue
		}
		b.WriteString(escape(line[text:i]))
		b.WriteString(line[i : end+n])
		i, text = end+n, end+n
	}
	b.WriteString(escape(line[text:]))
	return b.String()
}

// ticks is the length of the run of backticks at i.
func ticks(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// closer is where the first run of exactly n backticks from i starts, or -1.
func closer(s string, i, n int) int {
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		run := ticks(s, i)
		if run == n {
			return i
		}
		i += run
	}
	return -1
}

func escape(s string) string {
	return masks.ReplaceAllString(s, "&lt;$1&gt;")
}
