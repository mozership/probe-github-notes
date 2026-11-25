// Command probe-github-notes serves a Probe action that puts the notes of a
// run on GitHub: in a comment on a pull request, in the summary of the
// GitHub Actions job, or in an issue. Probe starts it for a step that uses
// github.com/mozership/probe-github-notes@<commit>.
package main

import (
	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe/sdk"
)

// version is set at build time.
var version = "dev"

func main() {
	sdk.Serve(func(log hclog.Logger) sdk.Action {
		return &Action{log: log}
	})
}
