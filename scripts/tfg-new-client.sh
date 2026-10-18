#!/usr/bin/env bash
# Start a TFG client install.
#
#   scripts/tfg-new-client.sh <slug> "<Company Name>" "<Operator first name>" [domain]
#   e.g. scripts/tfg-new-client.sh acme "Acme Logistics" "Sarah" acme.com
#
# What it does (nothing leaves this machine):
#   1. creates git branch client/<slug> from the current TFG branch
#   2. writes config/tfg/clients/<slug>.yaml from the template
#   3. regenerates the brand files for that client and commits
# Then follow docs/tfg/CLIENT-INSTALL.md (edit the brand file with the client,
# run the stack, `make client OWNER=<email>`).
set -euo pipefail

slug="${1:-}"; company="${2:-}"; operator="${3:-}"; domain="${4:-example.com}"
if [[ -z "$slug" || -z "$company" || -z "$operator" ]]; then
  echo "usage: $0 <slug> \"<Company Name>\" \"<Operator first name>\" [domain]" >&2
  exit 2
fi
if [[ ! "$slug" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
  echo "slug must be lowercase letters, digits and dashes (got: $slug)" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
file="config/tfg/clients/$slug.yaml"
branch="client/$slug"

if [[ -n "$(git status --porcelain)" ]]; then
  echo "commit or stash your changes first (git status is not clean)" >&2
  exit 1
fi
if git rev-parse --verify --quiet "$branch" >/dev/null; then
  echo "branch $branch already exists" >&2
  exit 1
fi

# Short name: initials of the company, max 4 letters (edit later if needed).
short="$(echo "$company" | tr -cs '[:alnum:]' ' ' | awk '{for(i=1;i<=NF&&i<=4;i++) printf toupper(substr($i,1,1))}')"

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
python3 - "$tmp/brand.yaml" "$company" "$short" "$operator" "$domain" <<'PY'
import sys, pathlib
out, company, short, operator, domain = sys.argv[1:]
t = pathlib.Path("config/tfg/clients/_template.yaml").read_text()
for k, v in {"__COMPANY__": company, "__SHORT__": short, "__OPERATOR__": operator, "__DOMAIN__": domain}.items():
    t = t.replace(k, v.replace('"', "'"))
lines = t.splitlines(keepends=True)
# drop the template's "do not edit" header
while lines and lines[0].startswith("#"):
    lines.pop(0)
header = f"# Brand file for {company} (a TFG client install). Edit, then run make tfg-brand with BRAND set to this file.\n"
pathlib.Path(out).write_text(header + "".join(lines))
PY
python3 scripts/tfg-brand.py --brand "$tmp/brand.yaml" --validate   # fail before touching git
git switch -c "$branch"
cp "$tmp/brand.yaml" "$file"
python3 scripts/tfg-brand.py --brand "$file"
git add -A
git -c user.name="${GIT_AUTHOR_NAME:-TFG}" -c user.email="${GIT_AUTHOR_EMAIL:-thefoundersgroupadmin@gmail.com}" \
  commit -qm "Client install: $company"
echo
echo "Created $branch with $file."
echo "Next: docs/tfg/CLIENT-INSTALL.md (step 2 onwards)."
