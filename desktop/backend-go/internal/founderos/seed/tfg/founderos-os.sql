-- The Founders Group starting data: applied on top of the upstream demo
-- (seed/demo/founderos-os.sql) when seeding with --profile tfg (make tfg).
--
-- Rules for this file:
--   * No invented clients, leads, revenue or run history. The pipeline, run log
--     and finance views start empty and fill from real connectors, so every
--     number on screen is real.
--   * Upstream agents TFG does not use stay registered (the code expects them)
--     but are set to 'idle' with a note, so the board shows what is actually on.

-- ---- remove demo records ------------------------------------------------------
DELETE FROM funnel_touches;
DELETE FROM funnel_contacts;
DELETE FROM agent_runs;
DELETE FROM agent_messages;
DELETE FROM agent_tasks;
DELETE FROM broadcasts;
DELETE FROM broadcast_replies;
DELETE FROM cron_runs;
DELETE FROM comms_digests;
DELETE FROM digest_reads;
DELETE FROM plaud_ingests;
DELETE FROM contact_tags;
DELETE FROM usage_snapshots;
DELETE FROM metric_snapshots;
DELETE FROM social_accounts;
DELETE FROM social_snapshots;
DELETE FROM social_dms;
DELETE FROM social_dm_snapshots;
DELETE FROM social_dm_messages;
DELETE FROM social_posts;
DELETE FROM email_list_snapshots;
DELETE FROM lead_magnets;
DELETE FROM brand_deals;
DELETE FROM proposals;
DELETE FROM deliverable_decisions;
DELETE FROM trading_snapshots;
DELETE FROM trading_positions;
DELETE FROM trading_orders;
DELETE FROM trading_analysis;
DELETE FROM trading_activity;
DELETE FROM trading_limits;
DELETE FROM workflows;

-- ---- departments ----------------------------------------------------------------
UPDATE departments SET tagline = 'Pipeline, discovery calls, proposals, closes.' WHERE id = 'dept-sales';
UPDATE departments SET tagline = 'Content and attention that fills the pipeline.' WHERE id = 'dept-marketing-growth';
UPDATE departments SET tagline = 'AI SDRs, automations, the Brain, the stack.' WHERE id = 'dept-tech';
UPDATE departments SET tagline = 'Retainers in, costs out, one view.' WHERE id = 'dept-finance';
UPDATE departments SET tagline = 'Admin inbox, calendar, WhatsApp, Slack: one feed.' WHERE id = 'dept-comms';
UPDATE departments SET tagline = 'Every client onboarded, built, trained and served.' WHERE id = 'dept-clients';

-- ---- the team ---------------------------------------------------------------------
DELETE FROM sop_tasks WHERE assignee_kind = 'person';
DELETE FROM people;
INSERT INTO people VALUES('person-nader','dept-marketing-growth','Nader','CEO · sales and content lead','["gmail","calendar","slack","whatsapp"]');
INSERT INTO people VALUES('person-ilya','dept-sales','Ilya','Head of Sales · leads delivery','["gmail","calendar","fathom","crm"]');
INSERT INTO people VALUES('person-karim','dept-tech','Karim','CRM, product design and outbound','["crm","typeform","slack"]');

-- ---- agents -----------------------------------------------------------------------
-- TFG's AI SDR layer (internal/founderos/agents/roster/tfg). Read-only.
INSERT INTO agents VALUES('tfg-pipeline-sdr','dept-sales','Pipeline SDR','Daily follow-up queue','active','specialist','AI SDR: builds the day''s follow-up queue from both offers (open leads untouched for 3+ days, hottest first). It queues; a person sends.','rules + Postgres','["funnel"]','sales-agent','builtin');
INSERT INTO agents VALUES('tfg-crm-hygiene','dept-sales','CRM Hygiene','Pipeline record checks','active','specialist','AI SDR: lists pipeline records to fix (no email, no company, opted-in with no deal value, stuck 14+ days in one stage). It lists; a person fixes.','rules + Postgres','["funnel"]','sales-agent','builtin');
INSERT INTO agents VALUES('tfg-impact-reporter','dept-tech','Impact Reporter','Weekly proof of hours handed back','active','specialist','Last 7 days of agent runs, failures and model cost, and the hours handed back (successful runs × minutes per run from the brand file). Shows its formula.','rules + Postgres','["agent-runs"]','conductor','builtin');

-- Upstream modules TFG does not run. Still registered, shown as idle.
UPDATE agents SET status = 'idle', description = 'Not used by TFG (upstream FounderOS module). ' || description
 WHERE id IN ('brand-deal-agent','adsmith-creative','reelkit-editor','renderly-creative','dmflow-mcp','postly-publisher',
              'launchpad-cohort-sales','vantage-sales','paykit-sales','vantage-paykit','flexpay-financing');

-- ---- scheduled runs (only fire when FOUNDEROS_CRONS=1; times are server time) ------
UPDATE agent_crons SET enabled = 0;
UPDATE agent_crons SET enabled = 1 WHERE agent_id IN ('comms-digest','stack-monitor','client-onboarding');
INSERT INTO agent_crons VALUES('cron-tfg-pipeline-sdr-0815','tfg-pipeline-sdr','15 8 * * 1-5','Weekday follow-up queue for Ilya and Nader before the first call',1,'2026-10-18T00:00:00.000Z');
INSERT INTO agent_crons VALUES('cron-tfg-crm-hygiene-mon','tfg-crm-hygiene','0 8 * * 1','Monday pipeline clean-up list for Karim',1,'2026-10-18T00:00:00.000Z');
INSERT INTO agent_crons VALUES('cron-tfg-impact-fri','tfg-impact-reporter','0 16 * * 5','Friday impact report: runs, failures, cost, hours handed back',1,'2026-10-18T00:00:00.000Z');

-- ---- SOPs ---------------------------------------------------------------------------
DELETE FROM sop_tasks WHERE assignee_id IN ('brand-deal-agent','adsmith-creative','reelkit-editor','renderly-creative','dmflow-mcp','postly-publisher','launchpad-cohort-sales','vantage-sales','paykit-sales','vantage-paykit','flexpay-financing');
INSERT INTO sop_tasks VALUES('sop-tfg-pipeline-sdr','dept-sales','Build the daily follow-up queue','Open leads nobody has touched in 3+ days, hottest first.','["Read every open lead in both offers with its last touch","Keep the ones quiet for 3 or more days","Sort by likelihood, then by longest quiet","Post the queue to the run log for Ilya and Nader","Never send: a person writes and sends every follow-up"]','agent','tfg-pipeline-sdr');
INSERT INTO sop_tasks VALUES('sop-tfg-crm-hygiene','dept-sales','Flag pipeline records to fix','Missing email, company, deal value, or stuck in a stage.','["Read every open lead in both offers","Flag missing email and missing company","Flag opted-in leads with no deal value","Flag leads in the same stage for 14+ days","Hand the list to Karim; the agent edits nothing"]','agent','tfg-crm-hygiene');
INSERT INTO sop_tasks VALUES('sop-tfg-impact','dept-tech','Report the week''s impact','Runs, failures, model cost and hours handed back.','["Read the last 7 days of agent runs","Count successful and failed runs per agent","Sum model cost","Hours handed back = successful runs x minutes per run (brand file)","Name any failing agent so it gets fixed first"]','agent','tfg-impact-reporter');
INSERT INTO sop_tasks VALUES('sop-nader','dept-marketing-growth','CEO: direction, content, closes','Owns the offer, content and the biggest deals.','["Morning: read the Comms Digest and the follow-up queue","Take the hottest follow-ups personally","Publish content that feeds the pipeline","Friday: read the Impact Reporter and decide next week''s focus"]','person','person-nader');
INSERT INTO sop_tasks VALUES('sop-ilya','dept-sales','Head of Sales: pipeline and delivery','Works the follow-up queue, runs discovery, leads delivery.','["Work the Pipeline SDR queue every weekday","Run discovery calls (recorded, so they land in the Brain)","Send proposals and move deals forward in the funnel","Lead each client build and the team training"]','person','person-ilya');
INSERT INTO sop_tasks VALUES('sop-karim','dept-tech','CRM, product design, outbound','Keeps the system clean and builds client CRMs.','["Monday: clear the CRM Hygiene list","Keep connectors healthy (Integrations and Doctor views)","Build and configure each client''s CRM and AI SDRs","Run outbound sequences"]','person','person-karim');
INSERT INTO sop_tasks VALUES('sop-tfg-10-day-build','dept-clients','10-day build and client-experience simulation','Before any outreach: build, then walk the whole client journey.','["Days 1-3: CRM, pipeline stages, intake form, booking link","Days 4-6: AI SDR agents on, follow-up and hygiene rules agreed","Days 7-8: run a fake lead from form to signed proposal","Days 9-10: fix every gap found, document it in the Brain","Only then switch on outreach"]','person','person-ilya');

-- ---- skills (playbooks) ---------------------------------------------------------------
DELETE FROM skills;
INSERT INTO skills VALUES('skill-tfg-followup','Daily follow-up queue','Sales','Turns a quiet pipeline into a short list of people to contact today.','tfg-pipeline-sdr','live','["funnel"]',unistr('---\u000aname: daily-follow-up-queue\u000adescription: Turns a quiet pipeline into a short list of people to contact today.\u000acategory: Sales\u000astatus: live\u000a---\u000a\u000a# Daily follow-up queue\u000a\u000aOpen leads untouched for 3+ days, hottest first. Queues only; a person sends.\u000a'),1);
INSERT INTO skills VALUES('skill-tfg-hygiene','CRM hygiene','Sales','Finds the records that make reporting lie.','tfg-crm-hygiene','live','["funnel"]',unistr('---\u000aname: crm-hygiene\u000adescription: Finds the records that make reporting lie.\u000acategory: Sales\u000astatus: live\u000a---\u000a\u000a# CRM hygiene\u000a\u000aNo email, no company, opted-in with no deal value, stuck 14+ days. Lists only.\u000a'),2);
INSERT INTO skills VALUES('skill-tfg-impact','Impact report','Operations','The weekly proof: hours handed back, with the formula shown.','tfg-impact-reporter','live','["agent-runs"]',unistr('---\u000aname: impact-report\u000adescription: The weekly proof of hours handed back, with the formula shown.\u000acategory: Operations\u000astatus: live\u000a---\u000a\u000a# Impact report\u000a\u000aSuccessful runs x minutes per run from the brand file. Failures named.\u000a'),3);
INSERT INTO skills VALUES('skill-tfg-discovery','Discovery call to proposal','Sales','Turns a recorded discovery call into a proposal draft for Ilya to edit.',NULL,'learning','["fathom","brain"]',unistr('---\u000aname: discovery-to-proposal\u000adescription: Turns a recorded discovery call into a proposal draft.\u000acategory: Sales\u000astatus: learning\u000a---\u000a\u000a# Discovery call to proposal\u000a\u000aHuman in the loop: Ilya edits and sends every proposal.\u000a'),4);
INSERT INTO skills VALUES('skill-tfg-onboarding','Client onboarding','Clients','Signed client to kickoff: brand file, workspace, CRM, AI SDRs.','client-onboarding','planned','["stripe","slack"]',unistr('---\u000aname: client-onboarding\u000adescription: Signed client to kickoff.\u000acategory: Clients\u000astatus: planned\u000a---\u000a\u000a# Client onboarding\u000a\u000aSee docs/tfg/CLIENT-INSTALL.md.\u000a'),5);

-- ---- tools (honest: nothing is "connected" until its key is in) ----------------------
DELETE FROM tools;
INSERT INTO tools VALUES('tool-optimal-engine','Optimal Engine (Brain)','Knowledge','available','#fafafa','TFG''s memory: SOPs, call notes, client builds. Runs with the stack.');
INSERT INTO tools VALUES('tool-gmail','Gmail (TFG admin inbox)','Comms','planned','#d4d4d4','thefoundersgroupadmin inbox into the unified feed. Needs an app password (INBOX_1_*).');
INSERT INTO tools VALUES('tool-gcal','Google Calendar','Comms','planned','#d4d4d4','Discovery-call bookings move leads to Call booked.');
INSERT INTO tools VALUES('tool-whatsapp','WhatsApp','Comms','planned','#a3a3a3','Team and client chats into the unified feed (macOS desktop app, read-only).');
INSERT INTO tools VALUES('tool-slack','Slack','Comms','planned','#a3a3a3','Client channels and team digest. Needs SLACK_BOT_TOKEN.');
INSERT INTO tools VALUES('tool-typeform','Typeform','CRM & Revenue','planned','#737373','Intake form: every submission opens a lead in the funnel.');
INSERT INTO tools VALUES('tool-fathom','Fathom','CRM & Revenue','planned','#737373','Recorded discovery calls move leads to Call held and land in the Brain.');
INSERT INTO tools VALUES('tool-stripe','Stripe','Payments','planned','#525252','Retainer payments mark clients converted and trigger onboarding.');
INSERT INTO tools VALUES('tool-docusign','DocuSign','CRM & Revenue','planned','#525252','Signed proposals.');
INSERT INTO tools VALUES('tool-claude','Claude (Anthropic API)','Orchestration','planned','#fafafa','Model for agents that draft. Off until ANTHROPIC_API_KEY is set; every run logs its cost.');

-- ---- roadmap ---------------------------------------------------------------------------
DELETE FROM roadmap_items;
DELETE FROM phases;
INSERT INTO phases VALUES('phase-1',1,'Wire TFG','["Admin Gmail + Calendar","Typeform intake","Fathom calls","Stripe","Slack / WhatsApp"]');
INSERT INTO phases VALUES('phase-2',2,'AI SDRs on our own pipeline','["Pipeline SDR daily","CRM Hygiene weekly","Impact Reporter weekly","Scheduled runs on (FOUNDEROS_CRONS=1)"]');
INSERT INTO phases VALUES('phase-3',3,'Client installs','["Brand file per client","10-day build + simulation","Weekly impact report to each client"]');
INSERT INTO phases VALUES('phase-4',4,'Scale','["Hosted deployment","Drafting agents with human approval","Case studies from the Brain"]');
INSERT INTO roadmap_items VALUES('rm-tfg-fork','TFG fork of FounderOS','2026-Q4','done','dept-tech','Brand file, TFG team, both offers, three AI SDR agents, client-install kit.','phase-1');
INSERT INTO roadmap_items VALUES('rm-tfg-keys','Connect the TFG accounts','2026-Q4','now','dept-tech','Gmail, Calendar, Typeform, Fathom, Stripe keys into ~/.founderos/.env or the Connections board.','phase-1');
INSERT INTO roadmap_items VALUES('rm-tfg-crons','Turn on scheduled agent runs','2026-Q4','next','dept-tech','After a week of manual runs look right, set FOUNDEROS_CRONS=1.','phase-2');
INSERT INTO roadmap_items VALUES('rm-tfg-first-client','First client install','2026-Q4','next','dept-clients','Run the client-install kit for the first signed client.','phase-3');
INSERT INTO roadmap_items VALUES('rm-tfg-hosting','Hosted deployment','2027-Q1','later','dept-tech','Move from a laptop to an always-on server so crons run 24/7.','phase-4');

-- ---- tasks on the board -------------------------------------------------------------
INSERT INTO agent_tasks VALUES('task-tfg-1','tfg-pipeline-sdr','Run once by hand and check the queue makes sense','open','2026-10-18T09:00:00.000Z','2026-10-18T09:00:00.000Z');
INSERT INTO agent_tasks VALUES('task-tfg-2','tfg-crm-hygiene','First clean-up pass once leads are in','open','2026-10-18T09:00:00.000Z','2026-10-18T09:00:00.000Z');
INSERT INTO agent_tasks VALUES('task-tfg-3','gmail-worker','Connect the TFG admin inbox','open','2026-10-18T09:00:00.000Z','2026-10-18T09:00:00.000Z');

-- ---- persona: the client template ---------------------------------------------------
INSERT INTO personas VALUES('persona-tfg-client',0,'TFG Client','Sales-led B2B team','A sales team that only sells: AI SDRs take the rest.','The OS TFG installs for each client: their pipeline in one funnel, AI SDRs that queue follow-ups and keep the CRM clean, and a weekly impact report showing the hours handed back. Built in 10 days, simulated end to end before outreach.','#d4a72c','Hours of non-selling work handed to AI SDRs per rep per week','[{"name":"Pipeline","focus":"Every lead followed up on time","agents":["Pipeline SDR","CRM Hygiene"]},{"name":"Proof","focus":"Weekly impact report the client can audit","agents":["Impact Reporter"]},{"name":"Comms","focus":"One feed for inbox, calendar and chat","agents":["Comms Digest","Comms Agent"]}]','["Gmail or Outlook","Google Calendar","Typeform","Fathom","Stripe","Slack"]','["Follow-ups due today","Records to fix","Hours handed back per week","Discovery calls held","Proposals sent"]','Call notes, SOPs and the client''s playbook, so new reps ramp from the Brain.','The Friday impact report: one number the client can check.');
