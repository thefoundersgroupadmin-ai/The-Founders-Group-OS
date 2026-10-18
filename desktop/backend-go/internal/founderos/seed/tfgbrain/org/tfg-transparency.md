---
title: Transparency rules
kind: org
generated: tfg
---

# Transparency rules

How TFG runs agents for itself and for clients.

- No agent sends, posts, books or pays unless FOUNDEROS_WRITES=1, and TFG keeps it off by default.
- Scheduled runs only happen when FOUNDEROS_CRONS=1.
- Every run is stored with its summary, model and cost, and is counted on Home and visible on each agent's Org Chart card.
- Every connector shows its honest state: connected, not configured, or error. Nothing shows green unless it is.
- No invented records: the pipeline, run log and finance views start empty and fill from real accounts.
- Every number an agent reports carries the rule that produced it.
