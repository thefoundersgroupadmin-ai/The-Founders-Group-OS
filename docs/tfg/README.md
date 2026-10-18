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

## Safety switches (both off by default)

| Switch | What it allows | When to turn on |
|---|---|---|
| `FOUNDEROS_WRITES=1` | Any send, post or payment by an agent | Not needed for the TFG agents (they are read-only). Only if you later add drafting agents and want them to send. |
| `FOUNDEROS_CRONS=1` | Scheduled agent runs (Pipeline SDR weekdays 08:15, CRM Hygiene Mondays 08:00, Impact Reporter Fridays 16:00, plus Comms Digest, Stack Monitor, Onboarding) | After a week of running the agents by hand and the output looks right. |

Put them in `desktop/backend-go/.env` and run `make dev-local-restart`.

## Gates before you ship a change

```bash
make tfg-check     # brand files current, Go tests, frontend tests, svelte-check
```

One upstream frontend test (`os/brain/page.test.ts`, "one no-scroll screen")
times out when run on its own on slow machines. It fails the same way on
untouched upstream code and is not caused by the TFG changes.
