package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe/actionrpc"
)

// DefaultTimeout bounds a step whose with does not set timeout.
const DefaultTimeout = 30 * time.Second

// Where the action writes, as with.to names it.
const (
	toComment = "comment"
	toSummary = "summary"
	toIssue   = "issue"
)

// What the action did, as res.action says.
const (
	didCreate = "created"
	didUpdate = "updated"
	didClose  = "closed"
	didWrite  = "written"
	didNone   = "none"
)

// params are the keys the action takes in with. action.yml declares them,
// for probe check to report a key the action does not take.
var params = []string{"to", "notes", "body", "token", "repository", "number", "title", "labels", "key", "api_url", "timeout"}

// paramsOf are the keys each with.to takes, besides those all take.
var paramsOf = map[string][]string{
	toComment: {"token", "repository", "number", "key", "api_url"},
	toIssue:   {"token", "repository", "title", "labels", "key", "api_url"},
	toSummary: {},
}

// common are the keys every with.to takes.
var common = []string{"to", "notes", "body", "timeout"}

// keeps are the kinds of guard the action keeps to, as action.yml declares
// them: it writes to GitHub only when the run is not read-only, and connects
// only to a host the run allows.
var keeps = []string{actionrpc.KindReadOnly, actionrpc.KindAllowHost}

// keyPattern is what with.key may be, so that it can stand in the marker,
// an HTML comment.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Action puts the notes a step is given on GitHub.
type Action struct {
	log hclog.Logger
	// client sends the requests. It defaults to http.DefaultTransport.
	client *http.Client
	// getenv reads the environment. It defaults to os.Getenv.
	getenv func(string) string
}

// Run puts the notes in with on GitHub, under no guard.
func (a *Action) Run(with map[string]any) (map[string]any, error) {
	ret, _, err := a.RunStep(actionrpc.Call{With: with})
	return ret, err
}

// RunStep puts the notes in call.With on GitHub under the guard of the run.
func (a *Action) RunStep(call actionrpc.Call) (map[string]any, map[string]any, error) {
	if a.log == nil {
		a.log = hclog.NewNullLogger()
	}
	if a.getenv == nil {
		a.getenv = os.Getenv
	}
	actionrpc.LogParams(a.log, "received request parameters", call.With)

	start := time.Now()
	req, err := a.parseRequest(call.With)
	if err != nil {
		return nil, nil, err
	}
	if call.Guard.ReadOnly && req.to != toSummary {
		return nil, nil, actionrpc.Refuse("the run is read-only, and to: %s writes to GitHub; only to: summary writes under it", req.to)
	}

	ctx, cancel := context.WithTimeout(context.Background(), req.timeout)
	defer cancel()
	res, err := a.post(ctx, req, call.Guard)
	if err != nil {
		actionrpc.LogOutcome(a.log, "posting notes", nil, err)
		return nil, nil, err
	}
	ret := map[string]any{
		"req":    req.forResult(),
		"res":    res.forResult(),
		"rt":     time.Since(start).String(),
		"status": 0,
	}
	actionrpc.LogOutcome(a.log, "posting notes", ret, nil)
	return ret, nil, nil
}

type request struct {
	to         string
	notes      []actionrpc.Note
	body       string
	token      string
	repository string
	number     int
	title      string
	labels     []string
	key        string
	apiURL     *url.URL
	timeout    time.Duration
}

func (r *request) forResult() map[string]any {
	m := map[string]any{"to": r.to, "notes": len(r.notes)}
	if r.to != toSummary {
		m["repository"] = r.repository
		m["key"] = r.key
		m["api_url"] = r.apiURL.String()
	}
	if r.to == toComment {
		m["number"] = r.number
	}
	if r.to == toIssue {
		m["title"] = r.title
		m["labels"] = r.labels
	}
	return m
}

// result is what the action did.
type result struct {
	posted bool
	action string
	number int
	url    string
	id     int64
	code   int
}

func (r result) forResult() map[string]any {
	return map[string]any{
		"posted": r.posted,
		"action": r.action,
		"number": r.number,
		"url":    r.url,
		"id":     r.id,
		"code":   r.code,
	}
}

func (a *Action) parseRequest(with map[string]any) (*request, error) {
	r := &request{timeout: DefaultTimeout, key: "probe", title: "Probe notes", labels: []string{"probe"}}

	for k := range with {
		if !slices.Contains(params, k) {
			return nil, fmt.Errorf("github-notes action takes %s, not %s", strings.Join(params, ", "), k)
		}
	}

	r.to, _ = with["to"].(string)
	if _, ok := paramsOf[r.to]; !ok {
		return nil, fmt.Errorf("with.to must be comment, summary or issue, not %v", with["to"])
	}
	// The keys of the other kinds of to are not read, rather than refused,
	// so that the defaults of a job can give the token and the repository
	// to its steps of every kind.

	v, ok := with["notes"]
	if !ok {
		return nil, errors.New(`github-notes action requires with.notes, such as "{{ notes }}"`)
	}
	var err error
	if r.notes, err = parseNotes(v); err != nil {
		return nil, err
	}

	if r.body, err = optString(with, "body"); err != nil {
		return nil, err
	}

	if v, ok := with["timeout"]; ok {
		if r.timeout, err = actionrpc.ParseTimeout(v); err != nil {
			return nil, fmt.Errorf("with.timeout: %w", err)
		}
	}

	if r.to == toSummary {
		return r, nil
	}

	if r.token, err = optString(with, "token"); err != nil {
		return nil, err
	}
	if r.token == "" {
		return nil, fmt.Errorf("github-notes action requires with.token for to: %s", r.to)
	}

	if r.repository, err = optString(with, "repository"); err != nil {
		return nil, err
	}
	if r.repository == "" {
		r.repository = a.getenv("GITHUB_REPOSITORY")
	}
	if owner, name, ok := strings.Cut(r.repository, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		if r.repository == "" {
			return nil, errors.New("github-notes action requires with.repository, as owner/name, where GITHUB_REPOSITORY is not set")
		}
		return nil, fmt.Errorf("with.repository must be owner/name, not %q", r.repository)
	}

	if k, ok := with["key"]; ok {
		if r.key, ok = k.(string); !ok || !keyPattern.MatchString(r.key) {
			return nil, fmt.Errorf("with.key must be letters, digits, '.', '_' and '-', not %v", k)
		}
	}

	api, err := optString(with, "api_url")
	if err != nil {
		return nil, err
	}
	if api == "" {
		api = a.getenv("GITHUB_API_URL")
	}
	if api == "" {
		api = "https://api.github.com"
	}
	u, err := url.Parse(api)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("with.api_url must be an http or https URL, not %q", api)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	r.apiURL = u

	switch r.to {
	case toComment:
		if v, ok := with["number"]; ok {
			if r.number, err = number(with, "number"); err != nil || r.number <= 0 {
				return nil, fmt.Errorf("with.number must be the number of a pull request or an issue, not %v", v)
			}
		} else if r.number, err = a.eventNumber(); err != nil {
			return nil, err
		}
	case toIssue:
		if v, ok := with["title"]; ok {
			if r.title, ok = v.(string); !ok || strings.TrimSpace(r.title) == "" {
				return nil, fmt.Errorf("with.title must be a text that is not empty, not %v", v)
			}
		}
		if v, ok := with["labels"]; ok {
			if r.labels, err = stringList(v); err != nil {
				return nil, fmt.Errorf("with.labels: %w", err)
			}
		}
	}
	return r, nil
}

// eventNumber reads the number of the pull request of the event that
// started the GitHub Actions run, or 0 when the event is of none.
func (a *Action) eventNumber() (int, error) {
	path := a.getenv("GITHUB_EVENT_PATH")
	if path == "" {
		return 0, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("reading the event of the run: %w", err)
	}
	var ev struct {
		PullRequest *struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return 0, fmt.Errorf("reading the event of the run: %w", err)
	}
	if ev.PullRequest == nil {
		return 0, nil
	}
	return ev.PullRequest.Number, nil
}

func (a *Action) post(ctx context.Context, r *request, guard actionrpc.Guard) (result, error) {
	p := page{body: r.body, notes: r.notes, footer: a.runLink()}
	if r.to == toSummary {
		return a.writeSummary(p)
	}

	c := &client{http: a.httpClient(guard), base: r.apiURL, token: r.token, guard: guard}
	p.marker = fmt.Sprintf("<!-- probe-github-notes:%s -->", r.key)
	text, err := p.render(bodyLimit)
	if err != nil {
		return result{}, err
	}
	var res result
	if r.to == toComment {
		res, err = a.postComment(ctx, c, r, p.marker, text)
	} else {
		res, err = a.postIssue(ctx, c, r, p.marker, text)
	}
	res.code = c.code
	return res, err
}

func (a *Action) postComment(ctx context.Context, c *client, r *request, marker, text string) (result, error) {
	if r.number == 0 {
		a.log.Info("no pull request to comment on")
		return result{action: didNone}, nil
	}
	res := result{action: didNone, number: r.number}
	found, err := c.findComment(ctx, r.repository, r.number, marker)
	if err != nil {
		return res, err
	}
	var cm *comment
	switch {
	case found != nil:
		if cm, err = c.updateComment(ctx, r.repository, found.ID, text); err != nil {
			return res, err
		}
		res.action = didUpdate
	case len(r.notes) > 0:
		if cm, err = c.createComment(ctx, r.repository, r.number, text); err != nil {
			return res, err
		}
		res.action = didCreate
	default:
		return res, nil
	}
	res.posted, res.id, res.url = true, cm.ID, cm.HTMLURL
	return res, nil
}

func (a *Action) postIssue(ctx context.Context, c *client, r *request, marker, text string) (result, error) {
	res := result{action: didNone}
	found, err := c.findIssue(ctx, r.repository, r.labels, marker)
	if err != nil {
		return res, err
	}
	var is *issue
	switch {
	case found != nil:
		closing := len(r.notes) == 0
		if is, err = c.updateIssue(ctx, r.repository, found.Number, r.title, text, closing); err != nil {
			return res, err
		}
		res.action = didUpdate
		if closing {
			res.action = didClose
		}
	case len(r.notes) > 0:
		if is, err = c.createIssue(ctx, r.repository, r.title, text, r.labels); err != nil {
			return res, err
		}
		res.action = didCreate
	default:
		return res, nil
	}
	res.posted, res.id, res.number, res.url = true, is.ID, is.Number, is.HTMLURL
	return res, nil
}

// writeSummary appends the page to the summary of the GitHub Actions job.
func (a *Action) writeSummary(p page) (result, error) {
	path := a.getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		a.log.Info("GITHUB_STEP_SUMMARY is not set, so there is no summary to write to")
		return result{action: didNone}, nil
	}
	if len(p.notes) == 0 {
		return result{action: didNone}, nil
	}
	// The limit is that of the whole file, which earlier steps of the job
	// may have written to, and the page is written with a newline after it.
	l := summaryLimit
	if info, err := os.Stat(path); err == nil {
		l.taken = int(info.Size())
	}
	l.taken++
	text, err := p.render(l)
	if err != nil {
		return result{}, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return result{}, fmt.Errorf("writing the summary: %w", err)
	}
	_, err = f.WriteString(text + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return result{}, fmt.Errorf("writing the summary: %w", err)
	}
	return result{posted: true, action: didWrite}, nil
}

// runLink is a line linking to the GitHub Actions run, or empty outside one.
func (a *Action) runLink() string {
	server, repo, id := a.getenv("GITHUB_SERVER_URL"), a.getenv("GITHUB_REPOSITORY"), a.getenv("GITHUB_RUN_ID")
	if server == "" || repo == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("<sub>From [run %s](%s/%s/actions/runs/%s) of %s.</sub>", id, strings.TrimSuffix(server, "/"), repo, id, workflowName(a.getenv("GITHUB_WORKFLOW")))
}

func workflowName(s string) string {
	if s == "" {
		return "the workflow"
	}
	return "_" + s + "_"
}

// httpClient is the client of the action, which follows a redirect only to
// a host the guard allows.
func (a *Action) httpClient(guard actionrpc.Guard) *http.Client {
	c := http.Client{Transport: http.DefaultTransport}
	if a.client != nil {
		c = *a.client
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		// net/http sends a POST or a PATCH again as a GET after a 301 or a
		// 302, and the GET would answer with the comment or the issue as it
		// was, as though it had been written.
		if req.Method != via[0].Method {
			return fmt.Errorf("GitHub redirected a %s to %s, which would be sent as a %s; give api_url as the API answers it", via[0].Method, req.URL, req.Method)
		}
		return checkHost(guard, req.URL)
	}
	return &c
}

func optString(with map[string]any, key string) (string, error) {
	v, ok := with[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("with.%s must be a string, not %T", key, v)
	}
	return s, nil
}

func stringList(v any) ([]string, error) {
	switch l := v.(type) {
	case string:
		if strings.TrimSpace(l) == "" {
			return nil, nil
		}
		return []string{l}, nil
	case []any:
		out := make([]string, 0, len(l))
		for _, e := range l {
			s, ok := e.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("each label must be a text that is not empty, not %v", e)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("must be a list of labels, not %T", v)
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
