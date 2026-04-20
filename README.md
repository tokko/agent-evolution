# agent-evolution

A persistent, self-evolving dev team in a single Go binary.

- A kanban board in your browser, powered by `net/http` + plain HTML.
- Cards are natural-language tasks. A single "Genesis" agent picks them up, plans, writes code, runs it in a Docker sandbox, and commits production-ready changes to your target repo.
- Genesis can spawn new specialist roles (Coder, Reviewer, Tester, …) when recurring specialisms emerge, and can rewrite the daemon's own Go source via unified-diff patches (`edit_self`) with a clean build-and-handoff.
- One SQLite file stores tasks, roles, attempts, and the full event timeline.

No frameworks. Stdlib only, plus `modernc.org/sqlite` (pure Go, no CGO).

## Install (one-liner)

```bash
curl -fsSL https://raw.githubusercontent.com/tokko/agent-evolution/main/install.sh | bash
```

The script clones into `~/agent-evolution`, copies `.env.example` → `.env`,
builds `bin/daemon`, and (if Docker is present) builds the sandbox image.
Override with env vars:

```bash
AE_DIR=/opt/ae AE_SKIP_IMAGE=1 curl -fsSL .../install.sh | bash
```

| var | default | effect |
|---|---|---|
| `AE_DIR` | `$HOME/agent-evolution` | install target |
| `AE_REF` | `main` | git ref to check out |
| `AE_SKIP_BUILD` | — | set to `1` to skip `go build` |
| `AE_SKIP_IMAGE` | — | set to `1` to skip `docker build` |

After install, edit `.env` to set `MINIMAX_API_KEY`, then run
`bin/daemon --repo <your-target-repo>` and open `http://localhost:8080`.

## Manual setup

```bash
git clone https://github.com/tokko/agent-evolution.git
cd agent-evolution
cp .env.example .env
$EDITOR .env          # set MINIMAX_API_KEY
docker build -f sandbox.Dockerfile -t agent-sandbox:latest .
go build -o bin/daemon .
./bin/daemon --repo git@github.com:you/your-project.git
```

Drop a card on the board. Watch it move `todo → doing → done` as Genesis reads, writes, runs, commits, and pushes.

## Deploying on Raspberry Pi 5

```bash
docker build -f Dockerfile --platform linux/arm64 -t agent-evolution:latest .
docker run -d \
  --name agent-evolution \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v $PWD/state:/app \
  --env-file .env \
  agent-evolution:latest --repo git@github.com:you/your-project.git
```

The container needs the host's Docker socket to spin up sandboxes. The `/app`
volume persists `memory.db` and cloned workspaces across restarts.

## Environment

See `.env.example` for the full list. Most useful:

| var | default | notes |
|---|---|---|
| `MINIMAX_API_KEY` | — | required; your MiniMax cloud key |
| `MINIMAX_GROUP_ID` | — | optional GroupId query param |
| `MODEL` | `MiniMax-M2.7` | canonical model slug |
| `PORT` | `8080` | HTTP port |
| `SELF_MOD_ENABLED` | `true` | allow `edit_self` (Linux only) |
| `MAX_STEPS` | `25` | hard cap on agent-loop iterations per attempt |

## Architecture

```
browser ─HTTP─▶ server.go ─┬─▶ SQLite (tasks, roles, attempts, events)
                           │
                scheduler.go (one worker)
                           │
                        agent.go + tools.go
                           │
        ┌──────────┬───────┼────────┬──────────┐
        ▼          ▼       ▼        ▼          ▼
      llm.go   sandbox.go  gitops.go roles.go selfmod.go
 (MiniMax)   (docker run) (git)    (spawn)  (edit_self)
```

Each file has a single responsibility and is kept short so the LLM can read
and rewrite its own source. See the task-detail page for a full event
timeline per attempt (LLM request, tool call, sandbox run, commit, …).

## Safety notes

- The sandbox runs with `--network none`, a 30s hard timeout, and 256 MB of RAM.
- `write_repo` and `edit_self` reject paths that escape their respective roots.
- Commits and pushes are real — point `--repo` at a playground repo first.
- `edit_self` builds the new binary before swapping. On build failure it
  reverses the patch and reports the error to the LLM.

## Self-mod portability

`edit_self` handoff uses `syscall.Exec` and only works on Unix. Windows builds
compile fine (stub returns `ErrHandoffUnsupported`), so local development on
Windows is supported — you just cannot exercise the handoff path there.
