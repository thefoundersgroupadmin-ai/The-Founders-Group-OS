---
title: Impact Reporter
kind: agent
generated: tfg
---

# Impact Reporter

Operations agent in [[pillar-tech]]. Every Friday at 16:00 it reads the last seven days of agent runs and reports successful and failed runs per agent, total model cost, and the hours handed back.

Hours handed back = successful runs × minutes per run (set in the brand file, six by default). The formula is in every report, so a client can audit the number TFG sells on. Failing agents are named first so they get fixed. See [[sop-tfg-impact]].
