-- A TFG client install: applied after seed/tfg/founderos-os.sql when seeding
-- with --profile client. Keeps TFG's agents, rules and transparency defaults;
-- removes TFG's own team, SOPs and roadmap so the client starts clean.
-- Add the client's people from the People view (or a follow-up SQL file).

DELETE FROM sop_tasks WHERE assignee_kind = 'person';
DELETE FROM people;
DELETE FROM agent_tasks;
DELETE FROM roadmap_items;
DELETE FROM phases;
DELETE FROM personas WHERE id = 'persona-tfg-client';

UPDATE agent_crons SET description = 'Weekday follow-up queue for the sales team before the first call' WHERE id = 'cron-tfg-pipeline-sdr-0815';
UPDATE agent_crons SET description = 'Monday pipeline clean-up list for the CRM owner' WHERE id = 'cron-tfg-crm-hygiene-mon';

INSERT INTO sop_tasks VALUES('sop-client-10-day-build','dept-clients','10-day build and client-experience simulation','Run by TFG before any outreach starts.','["Days 1-3: CRM, pipeline stages, intake form, booking link","Days 4-6: AI SDR agents on, follow-up and hygiene rules agreed","Days 7-8: push a test lead from form to signed proposal","Days 9-10: fix every gap and document it in the Brain","Only then switch on outreach"]','agent','client-onboarding');

INSERT INTO phases VALUES('phase-1',1,'Build (days 1-10)','["Connect inbox, calendar, intake form, call recorder, payments","AI SDRs on","End-to-end simulation"]');
INSERT INTO phases VALUES('phase-2',2,'Run (months 1-3)','["Team trained on the daily queue","Weekly impact report reviewed with TFG","Scheduled runs on"]');
INSERT INTO phases VALUES('phase-3',3,'Hand over','["Team owns the system","TFG on retainer for changes"]');
INSERT INTO roadmap_items VALUES('rm-client-connect','Connect the client''s accounts','build','now','dept-tech','Keys into ~/.founderos/.env or the Connections board.','phase-1');
INSERT INTO roadmap_items VALUES('rm-client-sim','Run the end-to-end simulation','build','next','dept-clients','A test lead from form to signed proposal, every view checked.','phase-1');
INSERT INTO roadmap_items VALUES('rm-client-crons','Turn on scheduled runs','run','next','dept-tech','FOUNDEROS_CRONS=1 once a week of manual runs looks right.','phase-2');
