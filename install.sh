#!/usr/bin/env bash
# agent-evolution installer: minimal self-improving agent scaffold.
#
#   curl -fsSL https://raw.githubusercontent.com/tokko/agent-evolution/main/install.sh | bash
#
# Env vars (all optional):
#   AE_DIR         target directory (default: $HOME/agent-evolution)
#   AE_REF         git ref to check out (default: main)
#   AE_SKIP_BUILD  set to 1 to skip `go build`

set -euo pipefail

REPO_URL="https://github.com/tokko/agent-evolution.git"
DIR="${AE_DIR:-$HOME/agent-evolution}"
REF="${AE_REF:-main}"

say()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1 ($2)"
}

# --- detect platform -------------------------------------------------------

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  aarch64|arm64) arch="arm64" ;;
  x86_64|amd64)  arch="amd64" ;;
  *) die "unsupported arch: $arch (only arm64 and amd64 are tested)" ;;
esac
say "detected $os/$arch"

case "$os" in
  linux|darwin) ;;
  *) warn "unofficial OS: $os — proceeding, but the self-mod handoff path won't work" ;;
esac

# --- prerequisites ---------------------------------------------------------

need git "install with your package manager (apt, brew, ...)"
need go  "install Go 1.23+ from https://go.dev/dl/"

# --- clone or update -------------------------------------------------------

if [ -d "$DIR/.git" ]; then
  say "updating $DIR (ref=$REF)"
  git -C "$DIR" fetch --depth=1 origin "$REF"
  git -C "$DIR" checkout -q "$REF"
  git -C "$DIR" reset --hard "origin/$REF"
else
  say "cloning into $DIR (ref=$REF)"
  mkdir -p "$(dirname "$DIR")"
  git clone --depth=1 --branch "$REF" "$REPO_URL" "$DIR"
fi

cd "$DIR"

# --- .env bootstrap --------------------------------------------------------

if [ ! -f .env ]; then
  cp .env.example .env
  say "created $DIR/.env — edit it to set MINIMAX_API_KEY"
else
  say ".env already present, leaving it alone"
fi

# --- build -----------------------------------------------------------------

if [ "${AE_SKIP_BUILD:-}" = "1" ]; then
  warn "AE_SKIP_BUILD=1 — skipping go build"
else
  say "building daemon"
  mkdir -p bin
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/daemon .
  say "built $(pwd)/bin/daemon ($(du -h bin/daemon | awk '{print $1}'))"
fi

# --- next steps ------------------------------------------------------------

cat <<EOF

$(say 'installed.')

next steps:
  1) edit  $DIR/.env  and set MINIMAX_API_KEY (and MINIMAX_GROUP_ID if your account needs one)
  2) run   $DIR/bin/daemon
  3) watch $DIR/events.jsonl  (or tail -F it) to see what the agent is doing

the agent starts as a bare scaffold — it has no UI, no DB, no sandbox. Its
job is to read its own source and evolve itself, one edit_self diff at a
time, toward the target system described in system_prompt.go.

to update the scaffold itself later, rerun this same curl | bash, or:
  cd $DIR && git pull && go build -o bin/daemon .
EOF
