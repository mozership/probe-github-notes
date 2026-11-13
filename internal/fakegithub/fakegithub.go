// Package fakegithub is a stand-in for the part of the REST API of GitHub
// the action calls: the comments of an issue or a pull request, and the
// issues of a repository. It keeps them in memory, for the tests and the
// e2e workflows.
package fakegithub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Comment is a comment on an issue or a pull request.
type Comment struct {
	ID      int64  `json:"id"`
	Number  int    `json:"-"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}

// Issue is an issue, or a pull request when PullRequest is set.
type Issue struct {
	ID          int64          `json:"id"`
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	State       string         `json:"state"`
	Labels      []Label        `json:"labels"`
	HTMLURL     string         `json:"html_url"`
	PullRequest map[string]any `json:"pull_request,omitempty"`
}

// Label is a label of an issue.
type Label struct {
	Name string `json:"name"`
}

// Server keeps the comments and the issues of one repository.
type Server struct {
	// Repo is the repository, as owner/name; others are not found.
	Repo string
	// Token is the token a request must carry.
	Token string
	// PerPage is how many entries a page of a list holds, 100 when 0.
	PerPage int

	mu       sync.Mutex
	nextID   int64
	comments []*Comment
	issues   []*Issue
	// Requests counts the requests that reached the server.
	Requests int
}

// New returns a server of repo that takes token.
func New(repo, token string) *Server {
	return &Server{Repo: repo, Token: token, nextID: 1000}
}

// AddIssue adds an issue, or a pull request, as one written by someone
// else, and returns its number.
func (s *Server) AddIssue(is Issue) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	is.ID = s.id()
	is.Number = len(s.issues) + 1
	if is.State == "" {
		is.State = "open"
	}
	s.issues = append(s.issues, &is)
	return is.Number
}

// AddComment adds a comment on issue number, as one written by someone else.
func (s *Server) AddComment(number int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comments = append(s.comments, &Comment{ID: s.id(), Number: number, Body: body})
}

// Comments returns the comments on issue number, oldest first.
func (s *Server) Comments(number int) []Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Comment
	for _, c := range s.comments {
		if c.Number == number {
			out = append(out, *c)
		}
	}
	return out
}

// Issues returns every issue and pull request.
func (s *Server) Issues() []Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Issue, 0, len(s.issues))
	for _, is := range s.issues {
		out = append(out, *is)
	}
	return out
}

func (s *Server) id() int64 {
	s.nextID++
	return s.nextID
}

func (s *Server) issue(number int) *Issue {
	for _, is := range s.issues {
		if is.Number == number {
			return is
		}
	}
	return nil
}

// ServeHTTP answers the REST API of the repository.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests++
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		fail(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	prefix := "/repos/" + s.Repo + "/"
	rest, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	parts := strings.Split(rest, "/")
	switch {
	case len(parts) == 1 && parts[0] == "issues":
		switch r.Method {
		case http.MethodGet:
			s.listIssues(w, r)
		case http.MethodPost:
			s.createIssue(w, r)
		default:
			fail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		}
	case len(parts) == 2 && parts[0] == "issues":
		n, err := strconv.Atoi(parts[1])
		is := s.issue(n)
		if err != nil || is == nil || r.Method != http.MethodPatch {
			fail(w, http.StatusNotFound, "Not Found")
			return
		}
		s.updateIssue(w, r, is)
	case len(parts) == 3 && parts[0] == "issues" && parts[2] == "comments":
		n, err := strconv.Atoi(parts[1])
		if err != nil || s.issue(n) == nil {
			fail(w, http.StatusNotFound, "Not Found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.listComments(w, r, n)
		case http.MethodPost:
			s.createComment(w, r, n)
		default:
			fail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		}
	case len(parts) == 3 && parts[0] == "issues" && parts[1] == "comments" && r.Method == http.MethodPatch:
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		for _, c := range s.comments {
			if c.ID == id {
				var in struct{ Body string }
				if !decode(w, r, &in) {
					return
				}
				c.Body = in.Body
				reply(w, http.StatusOK, s.withURL(*c))
				return
			}
		}
		fail(w, http.StatusNotFound, "Not Found")
	default:
		fail(w, http.StatusNotFound, "Not Found")
	}
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request, number int) {
	var list []Comment
	for _, c := range s.comments {
		if c.Number == number {
			list = append(list, s.withURL(*c))
		}
	}
	page(w, r, s.perPage(r), list)
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request, number int) {
	var in struct{ Body string }
	if !decode(w, r, &in) {
		return
	}
	c := &Comment{ID: s.id(), Number: number, Body: in.Body}
	s.comments = append(s.comments, c)
	reply(w, http.StatusCreated, s.withURL(*c))
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	var labels []string
	if l := r.URL.Query().Get("labels"); l != "" {
		labels = strings.Split(l, ",")
	}
	var list []Issue
	for _, is := range s.issues {
		if state != "all" && is.State != state {
			continue
		}
		if !hasLabels(is, labels) {
			continue
		}
		list = append(list, s.issueWithURL(*is))
	}
	page(w, r, s.perPage(r), list)
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title, Body string
		Labels      []string
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Title == "" {
		fail(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	is := &Issue{ID: s.id(), Number: len(s.issues) + 1, Title: in.Title, Body: in.Body, State: "open"}
	for _, l := range in.Labels {
		is.Labels = append(is.Labels, Label{Name: l})
	}
	s.issues = append(s.issues, is)
	reply(w, http.StatusCreated, s.issueWithURL(*is))
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request, is *Issue) {
	var in struct {
		Title, Body *string
		State       *string
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Title != nil {
		is.Title = *in.Title
	}
	if in.Body != nil {
		is.Body = *in.Body
	}
	if in.State != nil {
		is.State = *in.State
	}
	reply(w, http.StatusOK, s.issueWithURL(*is))
}

func (s *Server) perPage(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if s.PerPage > 0 && (n == 0 || s.PerPage < n) {
		n = s.PerPage
	}
	if n <= 0 {
		n = 30
	}
	return n
}

func (s *Server) withURL(c Comment) Comment {
	c.HTMLURL = fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%d", s.Repo, c.Number, c.ID)
	return c
}

func (s *Server) issueWithURL(is Issue) Issue {
	is.HTMLURL = fmt.Sprintf("https://github.com/%s/issues/%d", s.Repo, is.Number)
	if is.Labels == nil {
		is.Labels = []Label{}
	}
	return is
}

func hasLabels(is *Issue, want []string) bool {
	for _, w := range want {
		if !slices.ContainsFunc(is.Labels, func(l Label) bool { return l.Name == w }) {
			return false
		}
	}
	return true
}

// page writes the page of list the request asks for, with a Link header to
// the next one as GitHub gives it.
func page[T any](w http.ResponseWriter, r *http.Request, per int, list []T) {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	start := min((p-1)*per, len(list))
	end := min(start+per, len(list))
	if end < len(list) {
		u := *r.URL
		u.Scheme, u.Host = "http", r.Host
		q := u.Query()
		q.Set("page", strconv.Itoa(p+1))
		u.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, u.String()))
	}
	out := list[start:end]
	if out == nil {
		out = []T{}
	}
	reply(w, http.StatusOK, out)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "Problems parsing JSON")
		return false
	}
	return true
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, message string) {
	reply(w, code, map[string]string{"message": message})
}
