// Command server is a stand-in for the REST API of GitHub, for the e2e
// workflow: the repository e2e/shop with pull request 1 open, which takes
// the token FAKE_GITHUB_TOKEN names, e2e-token when it is not set. It
// listens on FAKE_GITHUB_ADDR, 127.0.0.1:18082 when it is not set, and
// keeps what it is sent in memory.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/mozership/probe-github-notes/internal/fakegithub"
)

func main() {
	token := os.Getenv("FAKE_GITHUB_TOKEN")
	if token == "" {
		token = "e2e-token"
	}
	addr := os.Getenv("FAKE_GITHUB_ADDR")
	if addr == "" {
		addr = "127.0.0.1:18082"
	}
	fake := fakegithub.New("e2e/shop", token)
	fake.AddIssue(fakegithub.Issue{Title: "Add checkout", PullRequest: map[string]any{"url": "pulls/1"}})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL)
		fake.ServeHTTP(w, r)
	})
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
