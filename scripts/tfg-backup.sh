#!/usr/bin/env bash
# Nightly TFG OS database backup (run by the tfg-backup LaunchAgent).
#
#   scripts/tfg-backup.sh     # one dump now; keeps the newest $TFG_BACKUP_KEEP (14)
#
# Dumps go to iCloud Drive when it is on, so a lost laptop does not take them
# with it, otherwise to ~/.founderos/backups. Override with TFG_BACKUP_DIR.
# Restore: pg_restore --clean --if-exists -d "$DATABASE_URL" <file>.dump
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ICLOUD="$HOME/Library/Mobile Documents/com~apple~CloudDocs"
if [ -n "${TFG_BACKUP_DIR:-}" ]; then
  DIR="$TFG_BACKUP_DIR"
elif [ -d "$ICLOUD" ]; then
  DIR="$ICLOUD/TFG-OS-backups"
else
  DIR="$HOME/.founderos/backups"
fi
KEEP="${TFG_BACKUP_KEEP:-14}"
PG_DUMP="${TFG_PG_DUMP:-$(command -v pg_dump || echo /opt/homebrew/opt/postgresql@17/bin/pg_dump)}"

DB="${DATABASE_URL:-$(grep -E '^DATABASE_URL=' "$REPO/desktop/backend-go/.env" | tail -1 | cut -d= -f2- | tr -d '"')}"
[ -n "$DB" ] || { echo "no DATABASE_URL in desktop/backend-go/.env" >&2; exit 1; }

mkdir -p "$DIR"
chmod 700 "$DIR"
out="$DIR/tfg-os-$(date +%Y%m%d-%H%M%S).dump"
"$PG_DUMP" --format=custom --no-owner --file="$out.partial" "$DB"
mv "$out.partial" "$out"
chmod 600 "$out"
echo "backed up to $out"

# Keep the newest $KEEP dumps.
ls -1t "$DIR"/tfg-os-*.dump 2>/dev/null | tail -n +"$((KEEP + 1))" | while IFS= read -r old; do rm -f "$old"; done
