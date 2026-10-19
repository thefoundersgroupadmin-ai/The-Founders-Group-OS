# TFG OS: The Founders Group's fork of Founder OS

This is The Founders Group's sales command centre, built on the open-source
[Founder OS](https://github.com/Bennettxai/FounderOS) (MIT, on BusinessOS and the
Optimal Engine; credits and licence unchanged in `LICENSE` and the root `README.md`).

It does two jobs:

1. **Runs TFG.** Your two offers (Sales System Build, Fractional Sales + AI SDR),
   your team (Nader, Ilya, Karim), and three AI SDR agents on your own pipeline.
2. **Gets installed for each client.** The same system, rebranded from one file,
   seeded clean, with the same agents and transparency rules (see
   [CLIENT-INSTALL.md](CLIENT-INSTALL.md)).

How to keep it healthy: [MAINTENANCE.md](MAINTENANCE.md).

## What changed from upstream

| Area | Change |
|---|---|
| Branding | One file, `config/tfg/brand.yaml`, drives the company and product name, both offers' labels, colours, focus lists, lead routing, workspace names, hidden views and the hours formula. `scripts/tfg-brand.py` generates the Go and TypeScript files from it. |
| Agents | Three new read-only agents in `desktop/backend-go/internal/founderos/agents/roster/tfg`: **Pipeline SDR** (daily follow-up queue), **CRM Hygiene** (records to fix) and **Impact Reporter** (weekly hours handed back, with the formula shown). |
| Data | `make tfg` seeds TFG's team, SOPs, skills, tools, roadmap and Brain pages, and **removes every demo record**. The pipeline, run log and finances start empty and fill only from your real accounts. |
| Clients | `scripts/tfg-new-client.sh` and `make client` spin up a branded, clean install per client. |
| Upstream extras | Trading, Brand Deals and AdPilot are hidden. The upstream cohort pop-up is off. |

Internal keys `vantage` and `launchpad-cohort` are kept as stable database
slugs so upstream updates still merge. Users only ever see the labels from the
brand file.

## First run on a Mac

```bash
# 1. Tools (one time)
brew install node@22 go postgresql@17 pgvector redis elixir python ollama
corepack enable && corepack prepare pnpm@9.15.4 --activate
ollama pull nomic-embed-text          # the Brain's embeddings; Brain shows "offline" without it

# 2. Start everything
cd TFG-OS
BUSINESSOS_HEADLESS=1 make dev-local  # Postgres, Redis, Brain, backend, frontend
open http://localhost:5273/register   # create your account (use your real email)

# 3. Load TFG
make tfg OWNER=<the email you registered with>
make dev-local-restart                # the agent runtime picks up the new workspaces
open http://localhost:5273/os
```

`make dev-local-status` shows what is running; `make dev-local-stop` stops it.

### Optional: Graphify, so Claude spends fewer tokens on this code

```bash
brew install pipx && pipx install graphifyy
make graph                            # builds graphify-out/ (about 2 minutes)
```

Claude Code sessions in this folder then look symbols up in the map
(`graphify explain "PipelineSDR"`) instead of reading whole files. See
`CLAUDE.md`, "Graphify". Re-run `make graph` after big changes.

## Safety switches (both off by default)

| Switch | What it allows | When to turn on |
|---|---|---|
| `FOUNDEROS_WRITES=1` | Any send, post or payment by an agent | Not needed for the TFG agents (they are read-only). Only if you later add drafting agents and want them to send. |
| `FOUNDEROS_CRONS=1` | Scheduled agent runs (Pipeline SDR weekdays 08:15, CRM Hygiene Mondays 08:00, Impact Reporter Fridays 16:00, plus Comms Digest, Stack Monitor, Onboarding) | After a week of running the agents by hand and the output looks right. |

Put them in `desktop/backend-go/.env` and run `make dev-local-restart`.

## Paperclip live board integration

The **Real Agents** panel at `/os/agents` reads the separate Paperclip company
board. It does not mirror the Go FounderOS bridge roster: an agent registered in
one runtime does not automatically create an agent in the other.

For Paperclip running on this same Mac in `local_trusted` mode, first verify
its health endpoint and use its actual port (the default is often 3100; a local
installation may use another port). Replace `your-company-id` with the ID from
`GET /api/companies`, then add these settings to `~/.founderos/.env`:

```dotenv
PAPERCLIP_API_URL=http://127.0.0.1:3101
PAPERCLIP_COMPANY_ID=your-company-id
PAPERCLIP_AUTH_MODE=local_trusted
```

For this mode, leave `PAPERCLIP_BOARD_KEY` unset. The connector deliberately
omits the Authorization header and permits keyless access only when the URL is
plain HTTP on `localhost` or a loopback IP. Do **not** use this mode for a
tailnet, LAN, or remote Paperclip server. For a remote board, use its HTTPS URL,
a real board API key, and the company ID; leave `PAPERCLIP_AUTH_MODE` unset.

A healthy Paperclip service may still return an empty agent list. The connector
reports that as an error instead of inventing rows. Create/configure the real
Paperclip agents in Paperclip itself (start with the CEO, then its reports),
and ensure each has an adapter/model before enabling any heartbeat schedules.
The Go roster and the Paperclip board are distinct systems and must be verified
separately.

## Gates before you ship a change

```bash
make tfg-check     # brand files current, Go tests, frontend tests, svelte-check
```

One upstream frontend test (`os/brain/page.test.ts`, "one no-scroll screen")
times out when run on its own on slow machines. It fails the same way on
untouched upstream code and is not caused by the TFG changes.
