# agent-evolution

A minimal self-improving Go agent. It wakes up, reads its own source,
asks MiniMax (M2.7) for a single unified-diff improvement toward a target
system, applies the diff, rebuilds, and hands off to the new binary.

The **target system** is described in full in `system_prompt.go`. It is
a persistent kanban-board dev-team daemon — HTTP UI, SQLite, Docker
sandbox, git integration, spawnable specialist roles, self-mod handoff.
None of that exists yet. The agent's job is to build it.

The scaffold you start with has:

- ~5 source files (`main.go`, `loop.go`, `tools.go`, `system_prompt.go`,
  `llm.go`, `eventlog.go`, `selfmod*.go`).
- Six tools: `think`, `list_self`, `read_self`, `edit_self`, `done`, `fail`.
- Zero external dependencies — Go stdlib only.
- An append-only `events.jsonl` log so you can watch what the agent did.

No framework. No UI. No database. No sandbox. Those are the target, not
the starting point.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/tokko/agent-evolution/main/install.sh | bash
```

The script clones into `~/agent-evolution`, copies `.env.example` → `.env`,
and builds `bin/daemon`. Override with env vars:

| var | default | effect |
|---|---|---|
| `AE_DIR` | `$HOME/agent-evolution` | install target |
| `AE_REF` | `main` | git ref to check out |
| `AE_SKIP_BUILD` | — | set to `1` to skip `go build` |

## Manual setup

```bash
git clone https://github.com/tokko/agent-evolution.git
cd agent-evolution
cp .env.example .env
$EDITOR .env           # set MINIMAX_API_KEY
go build -o bin/daemon .
./bin/daemon
```

Tail the event log in another terminal:

```bash
tail -F events.jsonl
```

## What happens on first run

1. The daemon loads `.env`, opens `events.jsonl`, and builds a system
   prompt pinned to the absolute path of your source tree and your OS.
2. It sends one chat request to MiniMax: system prompt + "begin, survey
   your source, propose a small improvement."
3. The model must reply with a JSON object like
   `{"tool":"list_self","args":{}}`. Anything else gets three retries
   then the loop fails.
4. The daemon dispatches the tool call (reads files, applies a diff, …)
   and logs every step to `events.jsonl`.
5. When the model calls `edit_self`, the daemon:
   - runs `git apply --check` on the diff against the source tree,
   - runs `git apply` for real,
   - runs `go build -o bin/daemon.new .`,
   - on build failure, reverses the patch and returns the error to the
     model,
   - on success, `syscall.Exec`'s into the new binary with
     `--resume --self-src <dir> --log <path>` so the next process
     continues from the new source.
6. The loop runs up to `MAX_STEPS` (default 25) iterations in one
   process lifetime. After a successful `edit_self`, the counter resets
   in the new binary.

## Running on Raspberry Pi 5

Pi 5 is the intended deployment target. The binary is pure Go and
cross-compiles cleanly:

```bash
# on any machine:
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w" -o daemon-pi .
scp daemon-pi .env.example pi@<host>:~/agent-evolution/
```

On the Pi:

```bash
cd ~/agent-evolution
sudo apt install -y git golang-go    # the agent needs these to rewrite itself
cp .env.example .env
$EDITOR .env
./daemon-pi
```

(You can skip the `golang-go` apt install if you never plan to call
`edit_self` — the scaffold only needs Go at self-mod time.)

## Flags

| flag | default | meaning |
|---|---|---|
| `--self-src <dir>` | dir of binary / cwd | where to read+patch source |
| `--log <path>` | `./events.jsonl` | append-only JSONL event log |
| `--max-steps <n>` | 25 | cap on iterations per process |
| `--sleep <s>` | 0 | pause between steps |
| `--once` | false | run a single step then exit |
| `--resume` | false | internal: set across self-mod handoffs |

## Environment

| var | default | notes |
|---|---|---|
| `MINIMAX_API_KEY` | — | required; your MiniMax cloud key |
| `MINIMAX_GROUP_ID` | — | optional GroupId query param |
| `MODEL` | `MiniMax-M2.7` | model slug |
| `EVENT_LOG` | `./events.jsonl` | same as `--log` |
| `SELF_MOD_ENABLED` | `true` | set to `false` to disable `edit_self` |
| `MAX_STEPS` | `25` | iteration cap per process |

## Safety notes

- `edit_self` paths are safe-joined against the source directory. The
  agent cannot touch files outside its own tree.
- Build failure in `edit_self` reverts the patch (`git apply -R`) so the
  source tree is left untouched.
- Handoff only works on linux/darwin (`syscall.Exec`). On Windows,
  `edit_self` reports an error and the loop continues — useful for
  compile-checking but not for real evolution.
- This agent does **not** talk to a target git repo, does **not** run a
  sandbox, does **not** serve HTTP. It builds those itself. You can run
  it in an ordinary user account with no special permissions.
- The more permissive the model gets, the more you want this running in
  a VM or a container you don't care about. Start on a throwaway Pi.

## Architecture (current)

```
 main.go  ── flags, .env, signal shutdown
     │
     ▼
 loop.go  ─── build messages  ──► llm.go (MiniMax chat + tool parser)
     │       dispatch tool call
     ▼
 tools.go  ── think / list_self / read_self
     │                            / edit_self ──► selfmod.go (git apply + go build)
     ▼                                                │
 eventlog.go (append JSONL)                           ▼
                                          selfmod_{unix,windows}.go
                                             (syscall.Exec handoff)
```

The target architecture (what the agent is supposed to build) is
spelled out in `system_prompt.go`.

## v0.1.0 reference

The `v0.1.0` tag in this repo points at a prior "batteries-included"
implementation of the target system — kanban UI, scheduler, SQLite,
Docker sandbox, git pipeline. It exists as a worked example of the
goal. The current `main` branch is the minimal scaffold the agent
starts from. Don't try to merge them; let the agent re-derive its
own version of the target.
