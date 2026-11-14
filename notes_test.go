package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mozership/probe/actionrpc"
)

// mustRender renders p within the limit of a comment.
func mustRender(t *testing.T, p page) string {
	t.Helper()
	s, err := p.render(bodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func entry(job, step, status, level, title, body string) map[string]any {
	return map[string]any{
		"job": job, "job_id": strings.ToLower(job), "step": step, "index": 0,
		"status": status, "level": level, "title": title, "body": body,
	}
}

func TestRenderGroupsByJobAndStep(t *testing.T) {
	notes, err := parseNotes([]any{
		entry("Sign-up", "A new user signs up", "passed", "critical", "The form takes no + in an address", "Typing `qa+1@example.com`\nshows an error."),
		entry("Sign-up", "A new user signs up", "passed", "info", "", "The page took 3s."),
		entry("Checkout", "Pay", "failed", "warning", "Slow", "The total updates late."),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := mustRender(t, page{marker: "<!-- m -->", body: "Notes of the agent.", notes: notes, footer: "<sub>From the run.</sub>"})
	want := `<!-- m -->
Notes of the agent.

The run left 3 notes: 1 critical, 1 warning, 1 info.

### Sign-up

**A new user signs up** · passed

> [!CAUTION]
> **The form takes no + in an address**
>
> Typing ` + "`qa+1@example.com`" + `
> shows an error.

> [!NOTE]
> The page took 3s.

### Checkout

**Pay** · failed

> [!WARNING]
> **Slow**
>
> The total updates late.

<sub>From the run.</sub>
`
	if got != want {
		t.Errorf("render() =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderNoNotes(t *testing.T) {
	got := mustRender(t, page{marker: "<!-- m -->", notes: []actionrpc.Note{}})
	if got != "<!-- m -->\nThe run left no notes.\n" {
		t.Errorf("render() = %q", got)
	}
}

// TestRenderSplitsStepsOfARun checks that the notes of a step in two runs
// of a repeated job, and two steps of one name, stand under lines of their
// own, and that the run is shown.
func TestRenderSplitsStepsOfARun(t *testing.T) {
	a := entry("Load", "Get", "failed", "info", "", "one")
	a["run"] = 3.0
	b := entry("Load", "Get", "passed", "info", "", "two")
	b["index"] = 1
	notes, err := parseNotes([]any{a, b})
	if err != nil {
		t.Fatal(err)
	}
	got := mustRender(t, page{notes: notes})
	if strings.Count(got, "**Get**") != 2 || !strings.Contains(got, "**Get** · failed, run 3") {
		t.Errorf("render() =\n%s", got)
	}
}

func TestRenderLeavesOutWhatGitHubDoesNotTake(t *testing.T) {
	big := strings.Repeat("x", 20000)
	var list []any
	for range 5 {
		list = append(list, entry("J", "S", "passed", "info", "", big))
	}
	notes, err := parseNotes(list)
	if err != nil {
		t.Fatal(err)
	}
	got := mustRender(t, page{notes: notes, footer: "<sub>From the run.</sub>"})
	if n := utf8.RuneCountInString(got); n > bodyLimit.max {
		t.Errorf("render() is %d characters, want at most %d", n, bodyLimit.max)
	}
	if !strings.Contains(got, "2 more notes are left out, as GitHub takes at most 65536 characters here.") || strings.Count(got, big) != 3 || !strings.HasSuffix(got, "<sub>From the run.</sub>\n") {
		t.Errorf("render() does not keep 3 notes, say 2 are left out and end with the footer:\n%s", got[len(got)-300:])
	}

	// The summary of a step takes 1 MiB, so it has room for them all.
	sum, err := page{notes: notes}.render(summaryLimit)
	if err != nil || strings.Count(sum, big) != 5 || strings.Contains(sum, "left out") {
		t.Errorf("render(summaryLimit) error = %v, want every note in it", err)
	}
}

// TestRenderKeepsEveryNoteThatFits checks that a page with every note is
// kept when it fits, though the first note and the line saying the last
// is left out would not.
func TestRenderKeepsEveryNoteThatFits(t *testing.T) {
	list := []any{entry("J", "S", "passed", "info", "", "a"), entry("J", "S", "passed", "info", "", "b")}
	notes, err := parseNotes(list)
	if err != nil {
		t.Fatal(err)
	}
	short := mustRender(t, page{notes: notes})
	list[0].(map[string]any)["body"] = "a" + strings.Repeat("x", bodyLimit.max-utf8.RuneCountInString(short))
	if notes, err = parseNotes(list); err != nil {
		t.Fatal(err)
	}
	got := mustRender(t, page{notes: notes})
	if n := utf8.RuneCountInString(got); n != bodyLimit.max || strings.Contains(got, "left out") {
		t.Errorf("render() is %d characters, want both notes in exactly %d", n, bodyLimit.max)
	}
}

// TestRenderCountsCharacters checks that the limit of a comment is in
// characters, as GitHub counts it, not in bytes.
func TestRenderCountsCharacters(t *testing.T) {
	wide := strings.Repeat("あ", 25000)
	notes, err := parseNotes([]any{entry("J", "S", "passed", "info", "", wide), entry("J", "S", "passed", "info", "", wide)})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRender(t, page{notes: notes}); strings.Count(got, wide) != 2 {
		t.Errorf("render() left out a note of 25000 characters, %d bytes", len(wide))
	}
}

func TestRenderRefusesABodyThatDoesNotFit(t *testing.T) {
	notes, err := parseNotes([]any{entry("J", "S", "passed", "info", "", "b")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = page{marker: "<!-- m -->", body: strings.Repeat("x", bodyLimit.max), notes: notes}.render(bodyLimit)
	if err == nil || !strings.Contains(err.Error(), "with.body is too long") {
		t.Errorf("render() error = %v, want one saying with.body is too long", err)
	}
}

// TestRenderOfManyNotes checks that a page of many notes is written in
// time: each note is written and measured once.
func TestRenderOfManyNotes(t *testing.T) {
	list := make([]any, 20000)
	for i := range list {
		list[i] = entry("J", "S", "passed", "info", "", strings.Repeat("x", 100))
	}
	notes, err := parseNotes(list)
	if err != nil {
		t.Fatal(err)
	}
	got := mustRender(t, page{notes: notes})
	if !strings.Contains(got, "more notes are left out") {
		t.Errorf("render() of 20000 notes left none out")
	}
}

func TestParseNotesRefuses(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want string
	}{
		{"a note", "must be a list of notes"},
		{[]any{"x"}, "with.notes[0] must be an entry"},
		{[]any{entry("J", "S", "passed", "high", "", "b")}, `level "high" is not one of`},
		{[]any{map[string]any{"level": "info"}}, "body is missing"},
		{[]any{map[string]any{"level": "info", "body": "b", "title": 1}}, "title must be a string"},
		{[]any{map[string]any{"level": "info", "body": "b", "run": 1.5}}, "run must be a whole number"},
	} {
		if _, err := parseNotes(tc.v); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseNotes(%v) error = %v, want one with %q", tc.v, err, tc.want)
		}
	}
}

// TestRenderEscapesMasks checks that the placeholders of a body are
// written as text outside code, and left alone in it, a code span that goes
// on over the lines of a paragraph among it.
func TestRenderEscapesMasks(t *testing.T) {
	notes, err := parseNotes([]any{entry("J", "S", "passed", "info", "", "sent `<secret:TOKEN>` and <secret:TOKEN>\na `span\nwith <redacted>` and <redacted>\n\n```\ntoken=<redacted>\n```")})
	if err != nil {
		t.Fatal(err)
	}
	want := `> [!NOTE]
> sent ` + "`<secret:TOKEN>`" + ` and &lt;secret:TOKEN&gt;
> a ` + "`span" + `
> with <redacted>` + "`" + ` and &lt;redacted&gt;
>
> ` + "```" + `
> token=<redacted>
> ` + "```" + `
`
	if got := mustRender(t, page{notes: notes}); !strings.Contains(got, want) {
		t.Errorf("render() =\n%s\nwant it to have\n%s", got, want)
	}
}

func TestRenderEscapesMasksInNames(t *testing.T) {
	notes, err := parseNotes([]any{entry("Job <secret:HOST>", "Log in as <redacted>", "passed", "info", "Token <secret:TOKEN>", "b")})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRender(t, page{notes: notes}); !strings.Contains(got, "> **Token &lt;secret:TOKEN&gt;**") ||
		!strings.Contains(got, "### Job &lt;secret:HOST&gt;") || !strings.Contains(got, "**Log in as &lt;redacted&gt;**") {
		t.Errorf("render() =\n%s", got)
	}
}
