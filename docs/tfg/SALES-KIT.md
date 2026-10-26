# Sales Kit 0.3.0

Skills starts with a command box and durable command history, followed by Sales automation. The Conductor uses the same command path from every screen. The library contains 37 original workflows. All registered built-in workers and available task agents can be selected for a command; the model chooses relevant workers and workflows rather than launching every capability on every request.

## One command

Tell the Conductor an outcome, for example: “Review my sales pipeline, prioritise qualified US buyers and prepare the next outreach.” The active workspace travels with the command. The owner-only execution path validates worker IDs, workflow IDs and task-agent names against the actual registries. Commands persist before execution. Native workers run first, team tasks run sequentially, then shared workflows consolidate results. Progress, blockers and reports appear in Skills. Completed task documents are included in the report; the Conductor receives a completion turn and the owner notification feed receives a scoped event. Agent questions remain in Approvals; valid draft emails go to Outbox.

Tasks carry the approved profile, source records and workflow methods. Task agents also receive a shared local workflow catalogue and use `scripts/tfg-agent-context.py` to read saved context without relying on an authenticated browser session. Prospect and Outbox document imports retain the command workspace. Pipeline/Outbox sync jobs run every minute; reply routing runs every five minutes. No caller needs to trigger individual workflows.

## Configure once

Select a sales workspace, save its approved sales profile and timezone, and enable automatic analysis. Installation owners configure automatic work; authorised workspace members can read its reports. Client installations retain separate databases and credentials.

The backend polls every minute when FOUNDEROS_CRONS=1, without a browser. It reads the chosen workspace and source workspaces explicitly bound by the owner. TFG HQ can include the existing offer pipeline through those bindings without copying records. HQ also reads prospect-matched email previews, observed calendar events and its own knowledge search snippets when available. Installation inboxes are not silently shared with client workspaces. Cached records are not presented as live connector reads.

| Trigger | Automatic work |
|---|---|
| Pipeline snapshot change | Qualification, prospect research, outreach draft, SDR handoff draft |
| Call archive change | Transcript summary, follow-up draft, activity log draft |
| New local day | Daily briefing, pipeline review, CRM hygiene |
| New ISO week | Weekly review, win/loss analysis |
| Connected email/calendar change in HQ | Inbox review, calendar review and meeting proposals |

The first run can combine several routines into one model call. Unchanged records and completed review windows are skipped. Reports, source IDs, routine names and errors persist in the OS database. Assignments and activity changes remain proposals. Structured outreach drafts are resolved against authorised, eligible contacts and stored as pending Outbox items. Existing outreach is suppressed; this path never sends.

Migration 168 adds workspace-scoped configuration and run tables. Migration 169 adds explicit source bindings and durable command records. Migration 170 preserves sourced company websites, location, buyer roles, full research evidence and proposed next actions; unknown next-action and close dates remain null. Reads require installation ownership, workspace ownership or active membership. Configuration is installation-owner-only. Background calls retain the authorising person's identity and stop when that person is no longer the installation owner.

## Duplicate work and failures

A unique workspace/batch key claims work before inference. Checkpoints advance only after the report is saved successfully. Failed or interrupted packets are not automatically retried. Commands interrupted during execution are marked interrupted at restart; queued work and waiting task receipts persist. Atomic claims and per-request UUIDs prevent duplicate command execution. Changed sources, review windows or profile can create new work. No provider failover is used.

The usage pause is checked before planning and immediately before inference. Missing official usage blocks AI. The 50% threshold persists a pause across resets. This guards Sales Kit, not every account-wide AI execution surface.

## Client package

Download from Skills → Individual workflow tools and client package, or export without a server:

```sh
cd desktop/backend-go
go run ./cmd/founderos-sales-kit -output /absolute/path/to/sales-kit-0.3.0.zip
```

The ZIP contains 37 instruction folders, manifest, synthetic profile and handoff guide. It does not contain a running scheduler. Automatic client execution requires the updated OS and migrations 168–169. Follow [CLIENT-INSTALL.md](CLIENT-INSTALL.md), and register client acceptance and delivery in Deliverables.

## Remaining work

Renewal contract ingestion, automatic CRM patch application and autonomous booking are not implemented. External services still require working accounts and credentials. All 37 workflows are available to commands and task agents; only relevant routines are scheduled automatically. Missing sources are reported as blockers. Sources are capped at 200 pipeline records and 20 calls; oversized content is labelled as an excerpt. Do not describe the current routines as all 37 autonomous tasks.

## Build staging

FOUNDEROS_WEB_BUILD_DIR selects adapter-node's output directory, allowing production builds outside frontend/build while the previous release keeps serving its own assets. Default output is unchanged.

## Capability reference

The scope was inspired by Salesforce in Claude's 37 sales skills and its documented beta analysis/draft behaviour. Our instructions are original. Reference pages checked 25 October 2026:

- https://www.salesforce.com/eu/claudeforce/
- https://help.salesforce.com/s/articleView?id=sales.skills_ref.htm&language=en_US&type=5
