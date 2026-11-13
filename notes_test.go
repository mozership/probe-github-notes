package main

import (
	"strings"
	"testing"
)

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
	got := page{marker: "<!-- m -->", body: "Notes of the agent.", notes: notes, footer: "<sub>From the run.</sub>"}.render()
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
	got := page{marker: "<!-- m -->", notes: []note{}}.render()
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
	got := page{notes: notes}.render()
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
	got := page{notes: notes}.render()
	if len(got) > maxBody {
		t.Errorf("len(render()) = %d, want at most %d", len(got), maxBody)
	}
	if !strings.Contains(got, "2 more notes are left out") || strings.Count(got, big) != 3 {
		t.Errorf("render() does not keep 3 notes and say 2 are left out:\n%s", got[len(got)-200:])
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

func TestEscapeMasks(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"the password <secret:SHOP_PASSWORD> fails", "the password &lt;secret:SHOP_PASSWORD&gt; fails"},
		{"Authorization: <redacted>", "Authorization: &lt;redacted&gt;"},
		{"sent `<secret:TOKEN>` and <secret:TOKEN>", "sent `<secret:TOKEN>` and &lt;secret:TOKEN&gt;"},
		{"``a ` <redacted>`` <redacted>", "``a ` <redacted>`` &lt;redacted&gt;"},
		{"```\ntoken=<secret:TOKEN>\n```\n<secret:TOKEN>", "```\ntoken=<secret:TOKEN>\n```\n&lt;secret:TOKEN&gt;"},
		{"~~~\n```\n<redacted>\n~~~", "~~~\n```\n<redacted>\n~~~"},
		{"<b>bold</b> and <secret:lower_case1>", "<b>bold</b> and &lt;secret:lower_case1&gt;"},
		{"an open ` <redacted>", "an open ` &lt;redacted&gt;"},
	} {
		if got := escapeMasks(tc.in); got != tc.want {
			t.Errorf("escapeMasks(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRenderEscapesMasksInTitles(t *testing.T) {
	notes, err := parseNotes([]any{entry("J", "S", "passed", "info", "Token <secret:TOKEN>", "b")})
	if err != nil {
		t.Fatal(err)
	}
	if got := (page{notes: notes}).render(); !strings.Contains(got, "> **Token &lt;secret:TOKEN&gt;**") {
		t.Errorf("render() =\n%s", got)
	}
}
