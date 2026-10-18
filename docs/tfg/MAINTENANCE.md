# Maintenance runbook

## Daily (2 minutes, whoever opens it first)

- **Home**: the status line under the greeting should show only green and grey
  items (agents live, idle). Anything red or amber (Optimal Engine offline, a
  connector down, a failed run): open **Doctor**.
- **Home → Operating volume**: runs OK and failed today. A failed run: open
  that agent's card on **Org Chart** (each card has a run button and its last
  result). The **Agents** view is the optional Paperclip board; it shows
  "board unreachable" unless you run Paperclip.

## Weekly (Karim, Monday, 15 minutes)

1. Clear the CRM Hygiene list.
2. **Connections**: anything in `error` usually means an expired key or
   password. Renew it.
3. **Usage**: model cost for the week, if any drafting agents use Claude.
4. Back up the database (below).

## Monthly (Karim, 30 minutes)

1. Pull upstream fixes:
   ```bash
   git remote add upstream https://github.com/Bennettxai/FounderOS.git   # once
   git fetch upstream && git switch tfg && git merge upstream/main
   make tfg-check
   ```
   Conflicts are almost always in files the brand generator owns. Re-run
   `python3 scripts/tfg-brand.py` and commit.
2. Merge `tfg` into every `client/*` branch (CLIENT-INSTALL.md, step 6).
3. Update tools: `brew upgrade`, then `make dev-local-restart`.
4. Rotate any API key older than 90 days.

## Backups

```bash
pg_dump -h 127.0.0.1 -p 25432 -U postgres businessos_dev | gzip > ~/tfg-os-$(date +%F).sql.gz
```

The Brain's own data lives in `.run/optimal-engine-data/`. Back it up weekly
with the stack stopped (`make dev-local-stop`), then start it again. Keep 4 weeks.

## Where things live

| What | Where |
|---|---|
| Branding, offers, hours formula | `config/tfg/brand.yaml` (then `python3 scripts/tfg-brand.py`) |
| TFG agents | `desktop/backend-go/internal/founderos/agents/roster/tfg/` |
| TFG starting data | `desktop/backend-go/internal/founderos/seed/tfg/` (+ `client/` for client installs) |
| TFG Brain pages | `desktop/backend-go/internal/founderos/seed/tfgbrain/` |
| Keys | `~/.founderos/.env` or the Connections board. Never in git. |
| Switches | `desktop/backend-go/.env` (`FOUNDEROS_WRITES`, `FOUNDEROS_CRONS`) |
| Logs | `.run/*.log` (`make dev-local-logs`) |

## Hosting

Today the stack runs on a laptop, so scheduled runs only happen while that
laptop is awake. When you want 24/7 runs (Roadmap, phase 4), move to an
always-on server using the repo's `docker-compose.production.yml` and
`docs/BUSINESSOS.md`. Use one server per client, or one per TFG plus clients
with separate databases. Never put two companies in one database.

## When something breaks

| Symptom | Fix |
|---|---|
| Brain shows offline | Ollama is not running or the model is missing: `ollama serve`, `ollama pull nomic-embed-text`. |
| Running an agent says "runtime unavailable" | Workspaces were created after the backend started: `make dev-local-restart`. |
| "This OS isn't set up for this account" | Run `make tfg OWNER=<that account's email>` (or `make client`). |
| Funnel says "not live yet" | No lead source connected. Add Typeform, Calendar, Fathom or Stripe keys. |
| A connector stays in `error` | Its key expired or lacks a permission. The Connections card says which. |
| `make tfg` fails on brand files | Someone edited the brand file without regenerating: `python3 scripts/tfg-brand.py`. |
