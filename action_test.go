package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mozership/probe-github-notes/internal/fakegithub"
	"github.com/mozership/probe/actionref"
	"github.com/mozership/probe/actionrpc"
)

const (
	repo  = "mozership/shop"
	token = "ghs_test"
)

// gh starts a fake GitHub with pull request 1 open, and returns it with an
// action whose environment is env.
func gh(t *testing.T, env map[string]string) (*fakegithub.Server, *httptest.Server, *Action) {
	t.Helper()
	fake := fakegithub.New(repo, token)
	fake.AddIssue(fakegithub.Issue{Title: "Add checkout", PullRequest: map[string]any{"url": "x"}})
	ts := httptest.NewServer(fake)
	t.Cleanup(ts.Close)
	a := &Action{getenv: func(k string) string { return env[k] }}
	return fake, ts, a
}

func someNotes() []any {
	return []any{
		entry("Sign-up", "A new user signs up", "passed", "critical", "No + in an address", "The form refuses `qa+1@example.com`."),
		entry("Checkout", "Pay", "failed", "warning", "", "The total updates late."),
	}
}

func with(ts *httptest.Server, to string, notes []any, more map[string]any) map[string]any {
	w := map[string]any{"to": to, "notes": notes}
	if to != toSummary {
		w["token"] = token
		w["repository"] = repo
		w["api_url"] = ts.URL
	}
	if to == toComment {
		w["number"] = 1
	}
	for k, v := range more {
		w[k] = v
	}
	return w
}

func res(t *testing.T, ret map[string]any) map[string]any {
	t.Helper()
	r, ok := ret["res"].(map[string]any)
	if !ok {
		t.Fatalf("ret = %v, want res in it", ret)
	}
	return r
}

func TestCommentIsAddedThenEdited(t *testing.T) {
	fake, ts, a := gh(t, nil)
	fake.AddComment(1, "LGTM")
	fake.AddComment(1, "Why does the comment start with <!-- probe-github-notes:probe -->?")

	ret, err := a.Run(with(ts, toComment, someNotes(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["action"] != didCreate || r["posted"] != true || r["code"] != http.StatusCreated || !strings.Contains(r["url"].(string), "#issuecomment-") {
		t.Errorf("res = %v, want a comment created", r)
	}

	ret, err = a.Run(with(ts, toComment, someNotes()[:1], nil))
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["action"] != didUpdate || r["number"] != 1 {
		t.Errorf("res = %v, want the comment updated", r)
	}

	comments := fake.Comments(1)
	if len(comments) != 3 || !strings.HasPrefix(comments[1].Body, "Why") {
		t.Fatalf("comments = %v, want the two of someone else, the one quoting the marker as it was, and one of the action", comments)
	}
	body := comments[2].Body
	if !strings.HasPrefix(body, "<!-- probe-github-notes:probe -->\n") || !strings.Contains(body, "1 critical") || strings.Contains(body, "Checkout") {
		t.Errorf("comment =\n%s\nwant the marker and the notes of the second run", body)
	}
}

func TestCommentsOfTwoKeys(t *testing.T) {
	fake, ts, a := gh(t, nil)
	for _, key := range []string{"agent", "agent", "load"} {
		if _, err := a.Run(with(ts, toComment, someNotes(), map[string]any{"key": key})); err != nil {
			t.Fatal(err)
		}
	}
	comments := fake.Comments(1)
	if len(comments) != 2 || !strings.Contains(comments[0].Body, "probe-github-notes:agent") || !strings.Contains(comments[1].Body, "probe-github-notes:load") {
		t.Errorf("comments = %v, want one of each key", comments)
	}
}

// TestCommentIsFoundOnALaterPage checks that the action reads the comments
// page by page, following the Link header.
func TestCommentIsFoundOnALaterPage(t *testing.T) {
	fake, ts, a := gh(t, nil)
	fake.PerPage = 2
	for range 5 {
		fake.AddComment(1, "a comment")
	}
	for range 2 {
		if _, err := a.Run(with(ts, toComment, someNotes(), nil)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(fake.Comments(1)); n != 6 {
		t.Errorf("%d comments, want 5 of others and 1 of the action", n)
	}
}

// TestSearchThatDoesNotEndFails checks that a step fails rather than add a
// second comment when the action gives up on the pages before the last.
func TestSearchThatDoesNotEndFails(t *testing.T) {
	fake, ts, a := gh(t, nil)
	fake.PerPage = 1
	for range maxPages + 1 {
		fake.AddComment(1, "a comment")
	}
	if _, err := a.Run(with(ts, toComment, someNotes(), nil)); err == nil || !strings.Contains(err.Error(), "looked through 30 pages") {
		t.Errorf("error = %v, want one saying the search did not end", err)
	}
	if n := len(fake.Comments(1)); n != maxPages+1 {
		t.Errorf("%d comments, want none added", n)
	}
}

func TestCommentWithNoNotes(t *testing.T) {
	fake, ts, a := gh(t, nil)

	ret, err := a.Run(with(ts, toComment, []any{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["action"] != didNone || r["posted"] != false || len(fake.Comments(1)) != 0 {
		t.Errorf("res = %v, comments = %v, want nothing posted", r, fake.Comments(1))
	}

	if _, err := a.Run(with(ts, toComment, someNotes(), nil)); err != nil {
		t.Fatal(err)
	}
	ret, err = a.Run(with(ts, toComment, []any{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	comments := fake.Comments(1)
	if r := res(t, ret); r["action"] != didUpdate || len(comments) != 1 || !strings.Contains(comments[0].Body, "The run left no notes.") || strings.Contains(comments[0].Body, "Sign-up") {
		t.Errorf("res = %v, comments = %v, want the comment to say there are no notes", r, comments)
	}
}

func TestCommentOnThePullRequestOfTheEvent(t *testing.T) {
	dir := t.TempDir()
	event := filepath.Join(dir, "event.json")
	if err := os.WriteFile(event, []byte(`{"action":"synchronize","pull_request":{"number":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fake, ts, a := gh(t, map[string]string{"GITHUB_EVENT_PATH": event, "GITHUB_REPOSITORY": repo, "GITHUB_API_URL": "unused"})
	w := map[string]any{"to": toComment, "notes": someNotes(), "token": token, "api_url": ts.URL}
	if _, err := a.Run(w); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.Comments(1)); n != 1 {
		t.Errorf("%d comments on the pull request of the event, want 1", n)
	}

	push := filepath.Join(dir, "push.json")
	if err := os.WriteFile(push, []byte(`{"ref":"refs/heads/main"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.getenv = func(k string) string {
		return map[string]string{"GITHUB_EVENT_PATH": push, "GITHUB_REPOSITORY": repo}[k]
	}
	requests := fake.Requests
	ret, err := a.Run(w)
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["posted"] != false || r["action"] != didNone || fake.Requests != requests {
		t.Errorf("res = %v, want nothing posted, and no request, for a push", r)
	}
}

func TestIssueIsOpenedEditedAndClosed(t *testing.T) {
	fake, ts, a := gh(t, nil)
	more := map[string]any{"title": "Notes of the nightly run", "labels": []any{"probe", "nightly"}}

	ret, err := a.Run(with(ts, toIssue, someNotes(), more))
	if err != nil {
		t.Fatal(err)
	}
	r := res(t, ret)
	if r["action"] != didCreate || r["number"] != 2 {
		t.Fatalf("res = %v, want issue 2 opened", r)
	}
	if _, err = a.Run(with(ts, toIssue, someNotes()[1:], more)); err != nil {
		t.Fatal(err)
	}
	ret, err = a.Run(with(ts, toIssue, []any{}, more))
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["action"] != didClose {
		t.Errorf("res = %v, want the issue closed", r)
	}
	ret, err = a.Run(with(ts, toIssue, someNotes(), more))
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["action"] != didCreate || r["number"] != 3 {
		t.Errorf("res = %v, want issue 3 opened after 2 was closed", r)
	}

	issues := fake.Issues()
	if len(issues) != 3 {
		t.Fatalf("issues = %v, want the pull request and two issues", issues)
	}
	closed := issues[1]
	if closed.State != "closed" || closed.Title != "Notes of the nightly run" || !strings.Contains(closed.Body, "The run left no notes.") {
		t.Errorf("issue 2 = %+v, want it closed, saying there are no notes", closed)
	}
	if names := []string{issues[2].Labels[0].Name, issues[2].Labels[1].Name}; !slices.Equal(names, []string{"probe", "nightly"}) {
		t.Errorf("labels = %v", names)
	}
}

// TestIssueOfAnotherLabelIsLeftAlone checks that the action edits only an
// open issue with its labels and marker.
func TestIssueOfAnotherLabelIsLeftAlone(t *testing.T) {
	fake, ts, a := gh(t, nil)
	fake.AddIssue(fakegithub.Issue{Title: "Copied", Body: "<!-- probe-github-notes:probe -->"})
	if _, err := a.Run(with(ts, toIssue, someNotes(), nil)); err != nil {
		t.Fatal(err)
	}
	if issues := fake.Issues(); len(issues) != 3 || issues[1].Body != "<!-- probe-github-notes:probe -->" {
		t.Errorf("issues = %v, want a new one opened beside the one without the label", issues)
	}
}

func TestIssueWithNoNotesAndNoneOpen(t *testing.T) {
	fake, ts, a := gh(t, nil)
	ret, err := a.Run(with(ts, toIssue, []any{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["action"] != didNone || len(fake.Issues()) != 1 {
		t.Errorf("res = %v, want no issue opened", r)
	}
}

func TestSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(path, []byte("# Earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Action{getenv: func(k string) string {
		return map[string]string{"GITHUB_STEP_SUMMARY": path, "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": repo, "GITHUB_RUN_ID": "42", "GITHUB_WORKFLOW": "E2E"}[k]
	}}
	ret, _, err := a.RunStep(actionrpc.Call{With: map[string]any{"to": toSummary, "notes": someNotes(), "body": "## Probe"}, Guard: actionrpc.Guard{ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["action"] != didWrite || r["posted"] != true {
		t.Errorf("res = %v, want the summary written", r)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	if !strings.HasPrefix(got, "# Earlier\n## Probe\n") || strings.Contains(got, "<!--") || !strings.Contains(got, "[run 42](https://github.com/mozership/shop/actions/runs/42) of _E2E_") {
		t.Errorf("summary =\n%s", got)
	}
}

// TestKeysOfOtherKindsAreLeftAlone checks that a key another to takes,
// such as the token the defaults of a job give, does not fail a step.
func TestKeysOfOtherKindsAreLeftAlone(t *testing.T) {
	a := &Action{getenv: func(string) string { return "" }}
	if _, err := a.Run(map[string]any{"to": toSummary, "notes": someNotes(), "token": token, "repository": repo, "title": "t"}); err != nil {
		t.Errorf("to: summary with the keys of comment and issue: error = %v", err)
	}
	_, ts, a := gh(t, nil)
	if _, err := a.Run(with(ts, toComment, someNotes(), map[string]any{"title": "t", "labels": []any{1}})); err != nil {
		t.Errorf("to: comment with the keys of issue: error = %v", err)
	}
}

// TestSummaryKeepsToWhatIsLeft checks that the limit of the summary is that
// of the whole file, of which earlier steps may have taken most.
func TestSummaryKeepsToWhatIsLeft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", summaryLimit.max-2000)), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Action{getenv: func(k string) string { return map[string]string{"GITHUB_STEP_SUMMARY": path}[k] }}
	notes := []any{entry("J", "S", "passed", "info", "", strings.Repeat("a", 1500)), entry("J", "S", "passed", "info", "", strings.Repeat("b", 1500))}
	if _, err := a.Run(map[string]any{"to": toSummary, "notes": notes}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if len(data) > summaryLimit.max || !strings.Contains(string(data), "1 more note is left out") {
		t.Errorf("the summary is %d bytes, want at most %d with the second note left out", len(data), summaryLimit.max)
	}

	if err := os.WriteFile(path, []byte(strings.Repeat("x", summaryLimit.max-10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(map[string]any{"to": toSummary, "notes": notes}); err == nil || !strings.Contains(err.Error(), "does not fit in what is left") {
		t.Errorf("error = %v, want one saying the page does not fit in what is left", err)
	}
}

func TestSummaryOutsideGitHubActions(t *testing.T) {
	a := &Action{getenv: func(string) string { return "" }}
	ret, err := a.Run(map[string]any{"to": toSummary, "notes": someNotes()})
	if err != nil {
		t.Fatal(err)
	}
	if r := res(t, ret); r["posted"] != false {
		t.Errorf("res = %v, want nothing written", r)
	}
}

func TestReadOnlyRefusesWritingToGitHub(t *testing.T) {
	for _, to := range []string{toComment, toIssue} {
		fake, ts, a := gh(t, nil)
		_, _, err := a.RunStep(actionrpc.Call{With: with(ts, to, someNotes(), nil), Guard: actionrpc.Guard{ReadOnly: true}})
		if !actionrpc.IsRefused(err) || fake.Requests != 0 {
			t.Errorf("to: %s under read-only: error = %v, %d requests; want it refused before any", to, err, fake.Requests)
		}
	}
}

func TestAllowHost(t *testing.T) {
	fake, ts, a := gh(t, nil)
	u, _ := url.Parse(ts.URL)

	_, _, err := a.RunStep(actionrpc.Call{With: with(ts, toComment, someNotes(), nil), Guard: actionrpc.Guard{AllowHosts: []string{"api.github.com"}}})
	if !actionrpc.IsRefused(err) || fake.Requests != 0 {
		t.Errorf("error = %v, %d requests; want it refused before any", err, fake.Requests)
	}
	if _, _, err := a.RunStep(actionrpc.Call{With: with(ts, toComment, someNotes(), nil), Guard: actionrpc.Guard{AllowHosts: []string{u.Host}}}); err != nil {
		t.Errorf("error = %v, want the host of api_url allowed", err)
	}
}

// TestRedirectOfAWriteFails checks that a PATCH redirected by a 301 is not
// sent again as a GET, which would read the comment as it was.
func TestRedirectOfAWriteFails(t *testing.T) {
	fake, ts, a := gh(t, nil)
	if _, err := a.Run(with(ts, toComment, someNotes(), nil)); err != nil {
		t.Fatal(err)
	}
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			http.Redirect(w, r, ts.URL+r.URL.Path, http.StatusMovedPermanently)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(moved.Close)
	_, err := a.Run(with(moved, toComment, someNotes()[:1], nil))
	if err == nil || !strings.Contains(err.Error(), "redirected a PATCH") {
		t.Errorf("error = %v, want one saying the PATCH was redirected", err)
	}
}

func TestAPIErrorFailsTheStep(t *testing.T) {
	_, ts, a := gh(t, nil)
	w := with(ts, toComment, someNotes(), map[string]any{"token": "wrong"})
	if _, err := a.Run(w); err == nil || !strings.Contains(err.Error(), "401 Unauthorized: Bad credentials") {
		t.Errorf("error = %v, want the status and the message of GitHub", err)
	}
	w = with(ts, toComment, someNotes(), map[string]any{"number": 9})
	if _, err := a.Run(w); err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Errorf("error = %v, want a 404", err)
	}
}

func TestDoesNotReturnNotes(t *testing.T) {
	_, ts, a := gh(t, nil)
	ret, err := a.Run(with(ts, toComment, someNotes(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res(t, ret)["notes"]; ok {
		t.Errorf("res = %v, want no notes in it, which Probe would leave again", ret["res"])
	}
}

func TestRequestRefuses(t *testing.T) {
	a := &Action{getenv: func(string) string { return "" }}
	base := func(more map[string]any) map[string]any {
		w := map[string]any{"to": toComment, "notes": []any{}, "token": token, "repository": repo, "number": 1}
		for k, v := range more {
			if v == nil {
				delete(w, k)
				continue
			}
			w[k] = v
		}
		return w
	}
	for _, tc := range []struct {
		with map[string]any
		want string
	}{
		{base(map[string]any{"to": "slack"}), "with.to must be comment, summary or issue"},
		{base(map[string]any{"to": nil}), "with.to must be"},
		{base(map[string]any{"titel": "x"}), "not titel"},
		{base(map[string]any{"notes": nil}), "requires with.notes"},
		{base(map[string]any{"notes": "{{ notes }}"}), "must be a list of notes"},
		{base(map[string]any{"token": nil}), "requires with.token for to: comment"},
		{base(map[string]any{"repository": nil}), "requires with.repository"},
		{base(map[string]any{"repository": "shop"}), "must be owner/name"},
		{base(map[string]any{"key": "a b"}), "with.key must be"},
		{base(map[string]any{"number": 0}), "with.number must be"},
		{base(map[string]any{"api_url": "ftp://x"}), "with.api_url must be"},
		{base(map[string]any{"timeout": "soon"}), "with.timeout"},
		{map[string]any{"to": toIssue, "notes": []any{}, "token": token, "repository": repo, "labels": []any{1}}, "with.labels"},
		{map[string]any{"to": toIssue, "notes": []any{}, "token": token, "repository": repo, "title": " "}, "with.title must be"},
	} {
		if _, err := a.Run(tc.with); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Run(%v) error = %v, want one with %q", tc.with, err, tc.want)
		}
	}
}

// TestManifestsDeclare checks that action.yml, and the one the e2e workflows
// run the action by, declare the params and the guard the action has, so
// that probe check and the guard of a run take it as it is. probe manifest
// keeps what action.yml declares in the one of a release.
func TestManifestsDeclare(t *testing.T) {
	for _, name := range []string{"action.yml", "e2e/action/action.yml"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		m, err := actionref.ParseManifest(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(m.Params, params) || !slices.Equal(m.Guard, keeps) {
			t.Errorf("%s declares params %v and guard %v, want %v and %v", name, m.Params, m.Guard, params, keeps)
		}
	}
}
