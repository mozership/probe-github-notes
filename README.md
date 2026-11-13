# probe-github-notes

A [Probe](https://github.com/mozership/probe) action that puts the notes of a run on GitHub: in a comment on a pull request, in the summary of the GitHub Actions job, or in an issue.

Actions such as [probe-agent](https://github.com/mozership/probe-agent) leave notes for people, besides whether their step passed, and a later step reads them as [`notes`](https://probe.linyo.ws/reference/yaml-configuration#notes), with the job and the step of each and the secrets hidden. Probe does not post them anywhere. This action does, at the time and with the notes the workflow chooses, and nothing else: it is not an action for GitHub at large.

It needs Probe v1.32.0 or later, the first with the `notes` context.

```yaml
name: Shop
actions:
  agent: github.com/mozership/probe-agent@<commit SHA>
  github-notes: github.com/mozership/probe-github-notes@<commit SHA>
secrets:
  - ANTHROPIC_API_KEY
  - GITHUB_TOKEN
vars:
  github_token: "{{GITHUB_TOKEN}}"
jobs:
- name: Agent
  id: agent
  steps:
  - name: A first-time shopper
    uses: agent
    with:
      url: https://staging.example.com/
      persona: Someone who has never used the site, on a phone
      goal: Find a gift under $30 and check out
    test: res.verdict == "pass"

- name: Notes
  needs: [agent]
  always: true
  steps:
  - name: Put the notes on the pull request
    uses: github-notes
    with:
      to: comment
      token: "{{vars.github_token}}"
      notes: "{{ notes }}"
```

The last job [`needs`](https://probe.linyo.ws/reference/yaml-configuration#needs) the others with [`always`](https://probe.linyo.ws/reference/yaml-configuration#always), so that it runs once they are over, however they ended, and reads the notes of them all.

The workflow gives the action a name once, under `actions`, and its steps use the name. Probe only takes a full 40-character commit SHA there. The notes of each [release](https://github.com/mozership/probe-github-notes/releases) start with the action and its commit to copy.

## Where the notes go

`to` chooses where the action writes.

| `to` | Where | On the next run | When there are no notes |
|---|---|---|---|
| `comment` | A comment on the pull request | The same comment is edited | The comment, if there is one, is edited to say so. None is added |
| `summary` | The summary of the GitHub Actions job | Each run of a job has a summary of its own | Nothing is written |
| `issue` | An open issue with `labels` | The same issue is edited, while it is open | The issue, if one is open, is edited to say so and closed. The next run with notes opens a new one |

A comment is for a workflow run on a change, read by those who review it. The summary needs no token, and is for a run with no pull request to comment on, or notes not for the reviewers. An issue is for a workflow run on a schedule, as against a staging environment each night, whose notes are work to track: it stays open while the runs leave notes, and is closed by the first that leaves none.

The action keeps one comment, or one open issue, for each `key`, by a marker at the start of its body, `<!-- probe-github-notes:<key> -->`. It looks through the comments of the pull request, or the open issues with `labels`, for the first that starts with the marker, and edits it; with none, it adds one. Two workflows, or two steps, keep comments of their own with keys of their own:

```yaml fragment
  - name: Every note, in one comment
    uses: github-notes
    with:
      to: comment
      token: "{{vars.github_token}}"
      notes: "{{ notes }}"

  - name: The critical notes, in a comment of their own
    uses: github-notes
    with:
      to: comment
      key: critical
      token: "{{vars.github_token}}"
      notes: "{{ filter(notes, .level == 'critical') }}"
```

Two runs that post to the same place at once can both find no comment and both add one, and a later run edits only the first. The action cannot keep separate runs apart, so keep the runs of a workflow from overlapping, as with the [`concurrency`](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency) of GitHub Actions:

```yaml fragment
concurrency:
  group: probe-notes-${{ github.event.pull_request.number || github.ref }}
```

## Parameters

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `to` | String | Yes | - | `comment`, `summary` or `issue` |
| `notes` | Array | Yes | - | Entries of the `notes` context, such as `"{{ notes }}"`. An empty list is a run with no notes |
| `body` | String | No | - | Markdown written above the notes |
| `timeout` | Duration | No | `30s` | Time limit for the step, as `10s` or a number of seconds |
| `token` | String | `comment`, `issue` | - | A token that can write comments, or issues, on the repository |
| `repository` | String | No | `GITHUB_REPOSITORY` | The repository, as `owner/name` |
| `key` | String | No | `probe` | Names the comment or the issue the action edits: letters, digits, `.`, `_` and `-` |
| `api_url` | String | No | `GITHUB_API_URL`, then `https://api.github.com` | The REST API, for GitHub Enterprise Server |
| `number` | Integer | No | The pull request of the event | `comment`: the pull request, or the issue, to comment on |
| `title` | String | No | `Probe notes` | `issue`: the title |
| `labels` | Array | No | `[probe]` | `issue`: the labels it has, by which the action finds it again |

The parameters of the other values of `to` are not read, so the [`defaults`](https://probe.linyo.ws/reference/yaml-configuration#defaults) of a job can give `token` to its steps of every kind. A key that is none of these fails the step, and `probe check` reports it with its line.

`notes` is a list of entries as the `notes` context has them; an entry without a `level` of `info`, `warning` or `critical`, or without a `body`, fails the step. Give it the context, or a part of it, as `filter(notes, .level != 'info')`, rather than a list written by hand.

With `to: comment` and no `number`, the action comments on the pull request of the event that started the GitHub Actions run, read from `GITHUB_EVENT_PATH`: a `pull_request` or a `pull_request_target` event. For any other, such as a push to a branch, the step passes without posting, and `res.posted` is false.

The token of a GitHub Actions run, `GITHUB_TOKEN`, needs the permissions to write: `pull-requests: write` for a comment on a pull request, and `issues: write` for an issue or a comment on one. List it under `secrets`, so that Probe hides it in what it prints.

## What it writes

A line counts the notes of each level, then the notes follow, under the name of their job and of their step, with the status of the step and, for a job with `repeat`, the run. Each note is a GitHub alert of its level, `CAUTION` for critical, `WARNING` for warning and `NOTE` for info, with its title and its body as they are.

```markdown
The run left 2 notes: 1 critical, 1 info.

### Agent

**A first-time shopper** · passed

> [!CAUTION]
> **The checkout button is under the cookie banner**
>
> On a 390px wide screen the banner covers the button until it is closed.

> [!NOTE]
> The search took 4s to show results.

<sub>From [run 123456](https://github.com/owner/name/actions/runs/123456) of _Shop_.</sub>
```

The last line links to the GitHub Actions run, and is left out outside one. A value Probe hides is `<secret:NAME>` or `<redacted>` in a note, which GitHub would take for an HTML tag and leave out, so the action writes it as text, except in code, where it shows as it is. GitHub takes at most 65536 characters in a comment or an issue, and 1 MiB in the summary of a step, of which earlier writes to it take their part, so the last notes of a longer page are left out, and a line says how many. A `body` too long to fit even without the notes fails the step.

## Result

| Field | Type | Description |
|---|---|---|
| `res.posted` | Boolean | Whether the action wrote anything |
| `res.action` | String | `created`, `updated`, `closed` (an issue), `written` (the summary) or `none` |
| `res.number` | Integer | The pull request or the issue posted to, or the pull request there was nothing to post on; `0` when there is none, and for the summary |
| `res.url` | String | The comment or the issue on GitHub, empty for the summary |
| `res.id` | Integer | The id of the comment or the issue |
| `res.code` | Integer | The status code of the last request to GitHub, `0` for the summary |
| `req` | Object | `to`, the number of `notes`, and what the action posted to, without the token |
| `rt` | Duration | How long the step took |
| `status` | Integer | `0` |

An error of GitHub, such as a 403 for a token that cannot write or a 404 for a pull request that is not there, fails the step with the status and the message of GitHub.

The action does not return the notes it puts on GitHub as `res.notes`, so that they are not left again for the steps after it.

## Guard

The action keeps to the guard of the run, and its `action.yml` declares `guard: [read-only, allow-host]`, so Probe runs it under `--read-only` and `--allow-host` without `--allow-action`.

- Under `--read-only`, `to: comment` and `to: issue` are refused before any request, as they write to GitHub. `to: summary` writes only to the page of the job, and runs.
- Under `--allow-host`, the host of `api_url`, and of each redirect, must be one the run allows: `api.github.com`, or `api.github.com:443`, for GitHub. A URL without a port is taken at the port of its scheme.

A refused step fails with the kind `refused`.

## Testing

`go test ./...` runs against a stand-in for the REST API of GitHub, in `internal/fakegithub`. `e2e/workflow.yml` runs the action through Probe: one job leaves notes, with [`hello`](https://probe.linyo.ws/reference/actions/hello), and another puts them on the stand-in served by `e2e/server`:

```sh
go run ./e2e/server &
go build -o e2e/action/probe-github-notes . && probe e2e/workflow.yml
```

`FAKE_GITHUB_URL` names the server when it listens elsewhere than on `127.0.0.1:18082`. `e2e/guard.yml` is run under `--read-only` and under `--allow-host`, where its comment is refused. In CI the summary is that of the e2e job.

## Releasing

Pushing a `v*` tag builds the executables with GoReleaser and publishes them on the release. The workflow then has `probe manifest` write an `action.yml` with their URLs and SHA-256 digests, commits it to `main`, and adds the commit to the release notes. The name, the description, `guard` and `params` are those of the `action.yml` at the tag, so that file is where to change them. That commit is the one to pin: Probe reads `action.yml` at the pinned commit and refuses an executable whose digest differs.

## License

MIT
