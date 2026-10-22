#!/usr/bin/env bash
# Set up the TFG OS background jobs on this Mac, for whoever is logged in.
#
#   scripts/tfg-setup-machine.sh            # install (safe to run again)
#   scripts/tfg-setup-machine.sh --dry-run  # print what it would do, change nothing
#   scripts/tfg-setup-machine.sh --remove   # stop and remove the jobs
#
# It copies the key templates into ~/.founderos (never over an existing file),
# builds the device collector, and writes one LaunchAgent per job with this
# Mac's own paths. A job whose keys are still blank is not loaded.
#
# Tests point it elsewhere with FOUNDEROS_HOME, LAUNCH_AGENTS_DIR and
# TFG_SKIP_LAUNCHCTL=1 / TFG_SKIP_BUILD=1.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FHOME="${FOUNDEROS_HOME:-$HOME/.founderos}"
AGENTS="${LAUNCH_AGENTS_DIR:-$HOME/Library/LaunchAgents}"
PY="${TFG_PYTHON:-/usr/bin/python3}"
MODE="${1:-install}"

# label | interval (s) | keys it needs, in file:KEY form
JOBS=(
  "paperclip-outbox|300|.env:PAPERCLIP_API_URL .env:PAPERCLIP_BOARD_KEY .env:PAPERCLIP_COMPANY_ID"
  "prospect-replies|900|.env:PAPERCLIP_API_URL .env:PAPERCLIP_BOARD_KEY .env:PAPERCLIP_COMPANY_ID planted.env:INBOX_1_HOST planted.env:INBOX_1_PASS"
  "paperclip-pipeline-sync|3600|.env:PAPERCLIP_API_URL .env:PAPERCLIP_BOARD_KEY .env:PAPERCLIP_COMPANY_ID"
  "founderos-collector|300|.env:FOUNDEROS_COLLECTOR_URL"
)

say() { printf '%s\n' "$*"; }
run() { if [ "$MODE" = "--dry-run" ]; then say "would run: $*"; else "$@"; fi; }

program_for() {
  case "$1" in
    paperclip-outbox) printf '%s\n' "$PY" -I "$REPO/scripts/sync-paperclip-outbox.py" ;;
    prospect-replies) printf '%s\n' "$PY" -I "$REPO/scripts/sync-prospect-replies.py" ;;
    paperclip-pipeline-sync) printf '%s\n' "$PY" -I "$REPO/scripts/sync-paperclip-pipeline.py" ;;
    founderos-collector) printf '%s\n' "$FHOME/bin/founderos-collector" -source-timeout 30s -deadline 180s ;;
  esac
}

# key_set FILE KEY: true when KEY has a non-empty value in FILE.
key_set() {
  [ -f "$1" ] && grep -E "^[[:space:]]*$2=" "$1" | tail -1 | cut -d= -f2- | tr -d '"[:space:]' | grep -q .
}

plist_for() {
  local label="net.thefoundersgroup.$1" interval="$2" args="" a
  while IFS= read -r a; do args+="<string>$a</string>"; done < <(program_for "$1")
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$label</string>
  <key>ProgramArguments</key><array>$args</array>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>/opt/homebrew/opt/node/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string></dict>
  <key>ProcessType</key><string>Background</string>
  <key>StartInterval</key><integer>$interval</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>$FHOME/logs/$1.out.log</string>
  <key>StandardErrorPath</key><string>$FHOME/logs/$1.err.log</string>
</dict></plist>
EOF
}

unload() {
  [ -n "${TFG_SKIP_LAUNCHCTL:-}" ] || [ "$MODE" = "--dry-run" ] && return 0
  launchctl bootout "gui/$(id -u)/net.thefoundersgroup.$1" 2>/dev/null || true
}

load() {
  [ -n "${TFG_SKIP_LAUNCHCTL:-}" ] && return 0
  launchctl bootstrap "gui/$(id -u)" "$AGENTS/net.thefoundersgroup.$1.plist"
}

if [ "$MODE" = "--remove" ]; then
  for job in "${JOBS[@]}"; do
    name="${job%%|*}"
    unload "$name"
    rm -f "$AGENTS/net.thefoundersgroup.$name.plist"
    say "removed $name"
  done
  exit 0
fi
case "$MODE" in install|--dry-run) ;; *) say "usage: $0 [--dry-run|--remove]" >&2; exit 2 ;; esac

run mkdir -p "$FHOME/logs" "$FHOME/bin" "$AGENTS"

for pair in "founderos.env.example:.env" "planted.env.example:planted.env"; do
  src="$REPO/config/tfg/machine/${pair%%:*}" dst="$FHOME/${pair##*:}"
  if [ -f "$dst" ]; then
    say "kept $dst"
  else
    run cp "$src" "$dst"
    run chmod 600 "$dst"
    say "created $dst: fill it in, then run this again"
  fi
done

if [ -z "${TFG_SKIP_BUILD:-}" ]; then
  say "building the device collector"
  run bash -c "cd '$REPO/desktop/backend-go' && go build -o '$FHOME/bin/founderos-collector' ./cmd/founderos-collector"
fi

missing_any=0
for job in "${JOBS[@]}"; do
  IFS='|' read -r name interval needs <<<"$job"
  missing=""
  for need in $needs; do
    key_set "$FHOME/${need%%:*}" "${need##*:}" || missing+=" ${need##*:}"
  done
  unload "$name"
  if [ -n "$missing" ]; then
    run rm -f "$AGENTS/net.thefoundersgroup.$name.plist"
    say "skipped $name: still blank:$missing"
    missing_any=1
    continue
  fi
  if [ "$MODE" = "--dry-run" ]; then
    say "would write $AGENTS/net.thefoundersgroup.$name.plist (every ${interval}s)"
    continue
  fi
  plist_for "$name" "$interval" >"$AGENTS/net.thefoundersgroup.$name.plist"
  load "$name"
  say "running $name (every ${interval}s)"
done

[ -f "$REPO/desktop/backend-go/.env" ] || say "note: desktop/backend-go/.env is missing; run 'make dev-local' once first, the syncs read DATABASE_URL from it"
[ "$MODE" = "--dry-run" ] && exit 0
[ "$missing_any" = 0 ] && say "all jobs running; logs in $FHOME/logs" || say "fill in the blank keys above, then run this again"
