# t3-bridge

A small daemon that connects GitHub issues to [t3](https://t3.codes) coding
sessions. Assign an issue to the configured user and a t3 session picks it up,
implements it in a fresh worktree, and opens a draft PR. When the owner
reviews the PR and requests changes, the same session wakes up and addresses
the feedback.

It runs on the same machine as the t3 server and drives it over the local
orchestration HTTP API (`/api/orchestration/dispatch` and
`/api/orchestration/shell`). GitHub access goes through the `gh` CLI, reusing
its existing authentication.

## How it works

Every tick (default 60s) the daemon runs one reconcile pass:

1. New open issue assigned to the configured user, with no session yet: the
   daemon cuts a git worktree on branch `t3/issue-N` from origin (using t3's
   own layout under `~/.t3/worktrees`), then dispatches `thread.create`,
   `thread.meta.update` (branch + worktree), and `thread.turn.start`. (The
   server's one-shot bootstrap exists only on its WebSocket RPC path, so the
   daemon does these steps itself.) The prompt tells the session to implement
   the issue, push the branch, and open a draft PR whose body contains
   `Fixes #N`. The daemon also comments on the issue so there is a visible
   record.
2. Session turn finished: the daemon finds the PR by its head branch and
   records it. If the turn completed without a PR it nudges the session once
   (the session may legitimately have declined and commented on the issue).
3. New review on the PR with state `CHANGES_REQUESTED` (or, with
   `reviewTrigger: "any_review"`, any substantive `COMMENTED` review): the
   daemon sends the review body plus all inline comments as a follow-up turn
   to the same thread. The session addresses the feedback, pushes to the same
   branch, and summarizes in a PR comment.
4. PR merged or closed: the thread is archived, which also stops the provider
   session.

State (issue -> thread -> PR, handled review ids) lives in a JSON file, so
restarts are idempotent. If state is lost, existing threads are re-adopted by
their branch name.

## Setup

```sh
# 1. Build and install
go build -o ~/.local/bin/t3-bridge ./cmd/t3-bridge

# 2. Mint a t3 API token (t3 server must be set up on this machine)
mkdir -p ~/.config/t3-bridge
npx t3 auth session issue --ttl 90d --label t3-bridge --token-only \
  > ~/.config/t3-bridge/token
chmod 600 ~/.config/t3-bridge/token

# 3. Configure
cp config.example.yaml ~/.config/t3-bridge/config.yaml
$EDITOR ~/.config/t3-bridge/config.yaml

# 4. Verify everything is wired up
t3-bridge doctor

# 5. Run it, either directly...
t3-bridge run
# ...or as a systemd user service
mkdir -p ~/.local/state/t3-bridge
cp systemd/t3-bridge.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now t3-bridge.service
```

Each watched repo must already exist as a project in t3 (the daemon matches
projects by `workspaceRoot` or `projectId`; `t3-bridge doctor` tells you if
the mapping is missing).

## Configuration

```yaml
t3:
  baseUrl: http://127.0.0.1:3773
  tokenFile: ~/.config/t3-bridge/token
state:
  file: ~/.local/state/t3-bridge/state.json
poll:
  intervalSeconds: 60
ui:
  listen: 127.0.0.1:3775
repos:
  - repo: owner/name
    assignee: github-login
    workspaceRoot: /path/to/local/checkout
    reviewTrigger: changes_requested
```

See `config.example.yaml` for all options (base branch, branch prefix,
model override, t3 project id).

- `assignee`: issues assigned to this login get picked up. Assigning requires
  triage permission or higher on the repo, so only trusted people can trigger
  sessions.
- `baseBranch`: defaults to the repo's default branch.
- `reviewTrigger`:
  - `changes_requested` (default): only reviews submitted with
    "Request changes" wake the session.
  - `any_review`: also substantive comment-only reviews. Use this when the
    reviewer is the same GitHub account that authors the PRs, because GitHub
    does not allow "Request changes" on your own PR. Empty synthetic reviews
    created by replying to inline threads are ignored, so the session
    replying to feedback cannot re-trigger itself.
- `model`: optional `{ "instanceId": "claudeAgent", "model": "..." }`
  override; defaults to the t3 project's default model.

## Monitoring UI

`t3-bridge run` serves a single-page monitoring UI (default
`http://127.0.0.1:3775`, configurable via `ui.listen`, `"off"` disables it).
It tails the daemon's events over SSE: session starts, PR discoveries,
forwarded reviews, errors. The header shows when the last reconcile tick ran
and counts down to the next one. The last 500 events are replayed on
connect.

To reach it from other machines on your tailnet without exposing it further,
put it behind Tailscale serve on a spare HTTPS port:

```sh
tailscale serve --bg --https=8443 http://127.0.0.1:3775
# -> https://<machine>.<tailnet>.ts.net:8443/
```

## Commands

- `t3-bridge run`: poll forever (daemon mode).
- `t3-bridge once`: single reconcile tick, useful for testing.
- `t3-bridge doctor`: check t3 reachability, token, gh auth, and the
  repo/project mapping.
- `-config <path>`: defaults to `~/.config/t3-bridge/config.yaml`.

## Operational notes

- The daemon never merges PRs and never marks them ready for review; the
  owner stays in the loop.
- If a session ends up waiting for human input in the t3 UI, the daemon logs
  it and leaves the thread alone.
- To retry a given-up issue (session errored, or finished without a PR
  twice), unassign and reassign the issue after deleting its entry from the
  state file, or fix things by hand in the t3 UI; the daemon adopts existing
  threads by branch name.
- Token expiry shows up as 401s in the log; re-mint with
  `npx t3 auth session issue` into the token file, no restart needed on the
  next tick.
