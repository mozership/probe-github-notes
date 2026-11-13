package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/mozership/probe/actionrpc"
)

// apiVersion is the version of the REST API the action is written against.
const apiVersion = "2022-11-28"

// maxPages bounds how many pages of comments or issues the action reads
// looking for its own, 100 to a page.
const maxPages = 30

// client calls the REST API of GitHub.
type client struct {
	http  *http.Client
	base  *url.URL
	token string
	guard actionrpc.Guard
	// code is the status code of the last response.
	code int
}

// comment is a comment on an issue or a pull request, and issue an issue,
// with what the action reads of them.
type comment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}

type issue struct {
	ID          int64  `json:"id"`
	Number      int    `json:"number"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PullRequest any    `json:"pull_request"`
}

// apiError is a response of GitHub other than 2xx.
type apiError struct {
	method, path string
	status       string
	message      string
}

func (e *apiError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("%s %s: GitHub answered %s", e.method, e.path, e.status)
	}
	return fmt.Sprintf("%s %s: GitHub answered %s: %s", e.method, e.path, e.status, e.message)
}

// findComment returns the first comment on issue number of repo whose body
// has marker, or nil.
func (c *client) findComment(ctx context.Context, repo string, number int, marker string) (*comment, error) {
	next := fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", repo, number)
	for range maxPages {
		var page []comment
		link, err := c.do(ctx, http.MethodGet, next, nil, &page)
		if err != nil {
			return nil, err
		}
		for i := range page {
			if strings.Contains(page[i].Body, marker) {
				return &page[i], nil
			}
		}
		if next = link; next == "" {
			return nil, nil
		}
	}
	return nil, fmt.Errorf("looked through %d pages of the comments of #%d without finding the comment of the action or the last page", maxPages, number)
}

func (c *client) createComment(ctx context.Context, repo string, number int, body string) (*comment, error) {
	var out comment
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues/%d/comments", repo, number), map[string]any{"body": body}, &out)
	return &out, err
}

func (c *client) updateComment(ctx context.Context, repo string, id int64, body string) (*comment, error) {
	var out comment
	_, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("repos/%s/issues/comments/%d", repo, id), map[string]any{"body": body}, &out)
	return &out, err
}

// findIssue returns the first open issue of repo with labels whose body has
// marker, or nil. Pull requests, which the API lists among issues, are
// passed over.
func (c *client) findIssue(ctx context.Context, repo string, labels []string, marker string) (*issue, error) {
	q := url.Values{"state": {"open"}, "per_page": {"100"}}
	if len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	next := fmt.Sprintf("repos/%s/issues?%s", repo, q.Encode())
	for range maxPages {
		var page []issue
		link, err := c.do(ctx, http.MethodGet, next, nil, &page)
		if err != nil {
			return nil, err
		}
		for i := range page {
			if page[i].PullRequest == nil && strings.Contains(page[i].Body, marker) {
				return &page[i], nil
			}
		}
		if next = link; next == "" {
			return nil, nil
		}
	}
	return nil, fmt.Errorf("looked through %d pages of the open issues without finding the issue of the action or the last page; give the issues of the action labels of their own", maxPages)
}

func (c *client) createIssue(ctx context.Context, repo, title, body string, labels []string) (*issue, error) {
	var out issue
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues", repo), payload, &out)
	return &out, err
}

// updateIssue sets the title and the body of issue number, and closes it
// when close is true.
func (c *client) updateIssue(ctx context.Context, repo string, number int, title, body string, close bool) (*issue, error) {
	var out issue
	payload := map[string]any{"title": title, "body": body}
	if close {
		payload["state"] = "closed"
	}
	_, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("repos/%s/issues/%d", repo, number), payload, &out)
	return &out, err
}

// linkNext reads the URL of the next page from a Link header.
var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// do sends a request to path, under the base URL, or to the absolute URL a
// Link header gave, and decodes the response into out. It returns the URL
// of the next page, if the response has one.
func (c *client) do(ctx context.Context, method, path string, payload, out any) (string, error) {
	u, err := c.base.Parse(path)
	if err != nil {
		return "", err
	}
	if err := checkHost(c.guard, u); err != nil {
		return "", err
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "probe-github-notes/"+version)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	c.code = resp.StatusCode
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return "", &apiError{method: method, path: u.Path, status: resp.Status, message: e.Message}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return "", fmt.Errorf("%s %s: the response is not the JSON expected: %w", method, u.Path, err)
		}
	}
	if m := linkNext.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
		return m[1], nil
	}
	return "", nil
}

// checkHost returns a Refused error when the guard does not allow the host
// of u. A host without a port is taken at the port of its scheme.
func checkHost(guard actionrpc.Guard, u *url.URL) error {
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	return guard.CheckHost(host)
}
