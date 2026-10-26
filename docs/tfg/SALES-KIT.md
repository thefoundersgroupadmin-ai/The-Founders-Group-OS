# Sales Kit 0.2.0

Skills starts with Sales automation. Individual workflow tools and package exports sit in an advanced section. The library contains 37 original instructions; four automatic routines currently orchestrate 12 of them.

## Configure once

Select a sales workspace, save its approved sales profile and timezone, and enable automatic analysis. Installation owners configure automatic work; authorised workspace members can read its reports. Client installations retain separate databases and credentials.

The backend polls every minute when FOUNDEROS_CRONS=1, without a browser. It reads only the chosen workspace's synced contacts, touches and archived calls. Existing sync jobs must populate those tables. Cached records are not presented as live connector reads.

| Trigger | Automatic work |
|---|---|
| Pipeline snapshot change | Qualification, prospect research, outreach draft, SDR handoff draft |
| Call archive change | Transcript summary, follow-up draft, activity log draft |
| New local day | Daily briefing, pipeline review, CRM hygiene |
| New ISO week | Weekly review, win/loss analysis |

The first run can combine several routines into one model call. Unchanged records and completed review windows are skipped. Reports, source IDs, routine names and errors persist in the OS database. Assignments, messages and activity changes remain drafts.

Migration 168 adds workspace-scoped configuration and run tables. Reads require installation ownership, workspace ownership or active membership. Configuration is installation-owner-only. Background calls retain the authorising person's identity and stop when that person is no longer the installation owner.

## Duplicate work and failures

A unique workspace/batch key claims work before inference. Checkpoints advance only after the report is saved successfully. Failed or interrupted packets are not automatically retried. Changed sources, review windows or profile can create new work. No provider failover is used.

The usage pause is checked before planning and immediately before inference. Missing official usage blocks AI. The 50% threshold persists a pause across resets. This guards Sales Kit, not every account-wide AI execution surface.

## Client package

Download from Skills → Individual workflow tools and client package, or export without a server:

```sh
cd desktop/backend-go
go run ./cmd/founderos-sales-kit -output /absolute/path/to/sales-kit-0.2.0.zip
```

The ZIP contains 37 instruction folders, manifest, synthetic profile and handoff guide. It does not contain a running scheduler. Automatic client execution requires the updated OS and migration 168. Follow [CLIENT-INSTALL.md](CLIENT-INSTALL.md), and register client acceptance and delivery in Deliverables.

## Remaining work

Automatic inbox/calendar and knowledge retrieval, renewal contract data, CRM patch application, sending and booking are not implemented. The other 25 library workflows are not automatically triggered yet. Sources are capped at 200 pipeline records and 20 calls; oversized content is labelled as an excerpt. Do not describe the current routines as all 37 autonomous tasks.

## Build staging

FOUNDEROS_WEB_BUILD_DIR selects adapter-node's output directory, allowing production builds outside frontend/build while the previous release keeps serving its own assets. Default output is unchanged.

## Capability reference

The scope was inspired by Salesforce in Claude's 37 sales skills and its documented beta analysis/draft behaviour. Our instructions are original. Reference pages checked 25 October 2026:

- https://www.salesforce.com/eu/claudeforce/
- https://help.salesforce.com/s/articleView?id=sales.skills_ref.htm&language=en_US&type=5
