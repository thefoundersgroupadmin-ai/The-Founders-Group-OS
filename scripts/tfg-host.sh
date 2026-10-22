#!/usr/bin/env bash
# Host TFG OS for the team from this Mac.
#
#   scripts/tfg-host.sh            # install (safe to run again)
#   scripts/tfg-host.sh --dry-run  # print what it would do, change nothing
#   scripts/tfg-host.sh --remove   # stop and remove the hosting jobs
#
# Four LaunchAgents:
#   tfg-os-stack   starts the backend, database, Redis and engine at login (headless)
#   tfg-os-web     the production web app on 127.0.0.1:$TFG_WEB_PORT, proxying /api
#                  to the backend; the Cloudflare tunnel publishes it as $TFG_PUBLIC_HOST
#   tfg-keep-awake stops the Mac sleeping while it is on power
#   tfg-backup     nightly database dump (scripts/tfg-backup.sh)
#   tfg-paperclip  the Paperclip agent back-end ($TFG_PAPERCLIP_DIR), when it is on
#                  this Mac: its runtime bundle (server/dist-runtime) if built, else
#                  from source; it reads its own ~/.paperclip instance config
#
# Build the web app first: cd frontend && pnpm build
# Tests point it elsewhere with LAUNCH_AGENTS_DIR, FOUNDEROS_HOME, TFG_SKIP_LAUNCHCTL=1
# and TFG_SKIP_BUILD=1.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FHOME="${FOUNDEROS_HOME:-$HOME/.founderos}"
AGENTS="${LAUNCH_AGENTS_DIR:-$HOME/Library/LaunchAgents}"
PUBLIC_HOST="${TFG_PUBLIC_HOST:-os.thefoundersgroup.net}"
WEB_PORT="${TFG_WEB_PORT:-5300}"
BACKEND_PORT="$(grep -E '^BACKEND_PORT=' "$REPO/.env.dev" 2>/dev/null | cut -d= -f2 || true)"
BACKEND_PORT="${BACKEND_PORT:-8801}"
NODE="${TFG_NODE:-$(command -v node || echo /opt/homebrew/bin/node)}"
MODE="${1:-install}"
PAPERCLIP_DIR="${TFG_PAPERCLIP_DIR:-$HOME/Downloads/paperclip}"
PAPERCLIP_PORT="${TFG_PAPERCLIP_PORT:-3101}"
JOBS=(tfg-os-stack tfg-os-web tfg-keep-awake tfg-backup)
[ -f "$PAPERCLIP_DIR/server/src/index.ts" ] && JOBS+=(tfg-paperclip)

say() { printf '%s\n' "$*"; }

# Paperclip states the Node it needs (package.json engines.node, ">=24.11.0").
# The shell's `node` can be older (node@22 first on PATH), and a Paperclip on
# too old a Node starts fine but fails every agent run, the Conductor's CEO
# included. So pick a Node that meets it, or refuse to install.
read -r -a PAPERCLIP_NODE_CANDIDATES <<<"${TFG_PAPERCLIP_NODE_CANDIDATES:-/opt/homebrew/opt/node/bin/node /opt/homebrew/opt/node@24/bin/node /usr/local/bin/node}"
paperclip_node_min() { sed -n 's/.*"node"[[:space:]]*:[[:space:]]*">=\([0-9.]*\)".*/\1/p' "$PAPERCLIP_DIR/package.json" 2>/dev/null | head -1; }
ver_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" = "$2" ]; }
paperclip_node() {
  if [ -n "${TFG_PAPERCLIP_NODE:-}" ]; then echo "$TFG_PAPERCLIP_NODE"; return 0; fi
  local want n v
  want="$(paperclip_node_min)"
  if [ -z "$want" ]; then echo "$NODE"; return 0; fi
  for n in "$NODE" "${PAPERCLIP_NODE_CANDIDATES[@]}"; do
    [ -x "$n" ] || continue
    v="$("$n" -v 2>/dev/null | tr -d 'v')"
    if [ -n "$v" ] && ver_ge "$v" "$want"; then echo "$n"; return 0; fi
  done
  say "Paperclip needs Node >= $want and none was found (tried $NODE ${PAPERCLIP_NODE_CANDIDATES[*]})." >&2
  say "Install one (brew install node) or set TFG_PAPERCLIP_NODE, then run this again." >&2
  return 1
}

xml() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' <<<"$1"; }

# plist NAME SCHEDULE ARG... (SCHEDULE: keepalive | login | nightly)
plist() {
  local name="$1" schedule="$2" args="" a env="" when workdir="$REPO" path="/opt/homebrew/opt/postgresql@17/bin:/opt/homebrew/opt/node/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
  shift 2
  for a in "$@"; do args+="<string>$(xml "$a")</string>"; done
  case "$name" in
    tfg-os-web) env="<key>PORT</key><string>$WEB_PORT</string><key>HOST</key><string>127.0.0.1</string><key>ORIGIN</key><string>https://$(xml "$PUBLIC_HOST")</string><key>BACKEND_INTERNAL_URL</key><string>http://127.0.0.1:$BACKEND_PORT</string><key>NODE_ENV</key><string>production</string>" ;;
    tfg-os-stack) env="<key>BUSINESSOS_HEADLESS</key><string>1</string>" ;;
    # its agents' tools (codex, npx) take node from PATH: the same Node first
    tfg-paperclip) workdir="$PAPERCLIP_DIR/server"; path="$(dirname "$1"):$path" ;;
  esac
  case "$schedule" in
    keepalive) when="<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer>" ;;
    login) when="<key>RunAtLoad</key><true/>" ;;
    nightly) when="<key>StartCalendarInterval</key><dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>15</integer></dict>" ;;
  esac
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>net.thefoundersgroup.$name</string>
  <key>ProgramArguments</key><array>$args</array>
  <key>WorkingDirectory</key><string>$(xml "$workdir")</string>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>$(xml "$path")</string>$env</dict>
  $when
  <key>StandardOutPath</key><string>$(xml "$FHOME/logs/$name.out.log")</string>
  <key>StandardErrorPath</key><string>$(xml "$FHOME/logs/$name.err.log")</string>
</dict></plist>
EOF
}

write_job() {
  case "$1" in
    tfg-os-stack) plist "$1" login /bin/bash "$REPO/scripts/dev-local.sh" start ;;
    tfg-os-web) plist "$1" keepalive "$NODE" "$REPO/frontend/build" ;;
    tfg-keep-awake) plist "$1" keepalive /usr/bin/caffeinate -s -i ;;
    tfg-backup) plist "$1" nightly /bin/bash "$REPO/scripts/tfg-backup.sh" ;;
    tfg-paperclip)
      # The self-contained bundle (cd $PAPERCLIP_DIR/server && pnpm build:runtime)
      # when there is one, else from source. server/dist alone does not run in
      # the monorepo: the workspace packages export their src/*.ts.
      local pnode
      pnode="$(paperclip_node)" || return 1
      if [ -f "$PAPERCLIP_DIR/server/dist-runtime/index.mjs" ]; then
        plist "$1" keepalive "$pnode" dist-runtime/index.mjs
      else
        plist "$1" keepalive "$pnode" --import tsx src/index.ts
      fi ;;
  esac
}

unload() {
  [ -n "${TFG_SKIP_LAUNCHCTL:-}" ] && return 0
  launchctl bootout "gui/$(id -u)/net.thefoundersgroup.$1" 2>/dev/null || true
}

load() {
  [ -n "${TFG_SKIP_LAUNCHCTL:-}" ] && return 0
  # bootout finishes asynchronously, so a bootstrap straight after it can fail.
  local i
  for i in 1 2 3 4 5; do
    launchctl bootstrap "gui/$(id -u)" "$AGENTS/net.thefoundersgroup.$1.plist" 2>/dev/null && return 0
    sleep 1
  done
  launchctl bootstrap "gui/$(id -u)" "$AGENTS/net.thefoundersgroup.$1.plist"
}

case "$MODE" in
  --remove)
    for name in "${JOBS[@]}"; do
      unload "$name"
      rm -f "$AGENTS/net.thefoundersgroup.$name.plist"
      say "removed $name"
    done
    exit 0 ;;
  --dry-run)
    for name in "${JOBS[@]}"; do say "would write $AGENTS/net.thefoundersgroup.$name.plist"; done
    say "web app: https://$PUBLIC_HOST -> 127.0.0.1:$WEB_PORT -> backend 127.0.0.1:$BACKEND_PORT"
    exit 0 ;;
  install) ;;
  *) say "usage: $0 [--dry-run|--remove]" >&2; exit 2 ;;
esac

[ -f "$REPO/frontend/build/index.js" ] || { say "no production build: run 'cd frontend && pnpm build' first" >&2; exit 1; }
mkdir -p "$FHOME/logs" "$AGENTS"
# The usage reporter teammates' laptops download (Usage → Connect this laptop).
if [ -z "${TFG_SKIP_BUILD:-}" ]; then
  mkdir -p "$FHOME/bin/laptop"
  for arch in arm64 amd64; do
    (cd "$REPO/desktop/backend-go" && CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -o "$FHOME/bin/laptop/tfg-usage-darwin-$arch" ./cmd/founderos-collector)
  done
  say "built the laptop usage reporter"
fi
for name in "${JOBS[@]}"; do
  # Write first: a job that can't be written (say no Node Paperclip accepts)
  # leaves the running one untouched instead of stopping it.
  tmp="$AGENTS/.net.thefoundersgroup.$name.plist.new"
  if ! write_job "$name" >"$tmp"; then
    rm -f "$tmp"
    say "left $name as it was" >&2
    exit 1
  fi
  unload "$name"
  mv "$tmp" "$AGENTS/net.thefoundersgroup.$name.plist"
  # A Paperclip started by hand already holds the port: leave it, and launchd
  # takes over at the next login.
  if [ "$name" = tfg-paperclip ] && [ -z "${TFG_SKIP_LAUNCHCTL:-}" ] && lsof -nP -iTCP:"$PAPERCLIP_PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    say "wrote $name; Paperclip is already running on :$PAPERCLIP_PORT, so it takes over at next login"
    continue
  fi
  load "$name"
  say "running $name"
done
say "web app on 127.0.0.1:$WEB_PORT; point the tunnel's $PUBLIC_HOST at http://localhost:$WEB_PORT"
