import { describe, expect, it } from 'vitest';
import { APPROVAL_BADGE, DOC_LABELS, STATUS_BADGE, actionsFor, parseDomains, setupChecklist, waitingReason } from './pods';
import type { Pod } from './types';

const pod = (over: Partial<Pod> = {}): Pod => ({
	slug: 'client-brightvale', clientName: 'Brightvale', package: 'core', reps: 6, status: 'setup',
	senderName: '', senderEmail: '', sendingDomain: '', approvalPolicy: 'client_approves_each',
	firstEmailDailyCap: 20, followUpDailyCap: 40, timezone: 'Europe/London', sendWindow: [9, 17], sendWeekends: false, dryRunInbox: '',
	crmProvider: '', calendarProvider: '', bookingUrl: '', pausedReason: '', portalClientId: '', wentLiveAt: null, ...over
});

describe('client pods view', () => {
	it('labels every status, colour meaning state', () => {
		expect(Object.keys(STATUS_BADGE).sort()).toEqual(['dry_run', 'handed_over', 'live', 'paused', 'setup']);
		expect(STATUS_BADGE.live.tone).toBe('ok');
		expect(STATUS_BADGE.paused.tone).toBe('warn');
		expect(STATUS_BADGE.dry_run.label).toBe('Test run');
	});

	it('offers only the status moves the backend allows', () => {
		const to = (s: Pod['status']) => actionsFor(pod({ status: s })).map((a) => a.to);
		expect(to('setup')).toEqual(['dry_run']);
		expect(to('dry_run')).toEqual(['live', 'paused', 'setup']);
		expect(to('live')).toEqual(['paused', 'handed_over']);
		expect(to('paused')).toEqual(['live', 'dry_run', 'handed_over']);
		expect(to('handed_over')).toEqual([]);
		expect(actionsFor(pod({ status: 'live' })).find((a) => a.to === 'paused')?.needsReason).toBe(true);
	});

	it('the setup checklist follows the pod, its mailbox, documents and agent', () => {
		const empty = setupChecklist(pod(), { configured: false }, {}, null);
		expect(empty.every((i) => !i.done)).toBe(true);
		expect(empty.map((i) => i.key)).toEqual(['mailbox', 'icp', 'voice', 'product', 'targets', 'agent', 'dryrun', 'live']);
		const ready = setupChecklist(
			pod({ status: 'live', senderEmail: 'sam@bv.example', wentLiveAt: '2026-11-09T10:00:00Z' }),
			{ configured: true },
			{ icp: { approved: true }, voice: { approved: true }, product: { approved: false }, targets: { approved: true } },
			{ agentId: 'a1' }
		);
		expect(ready.filter((i) => !i.done).map((i) => i.key)).toEqual(['product']);
		expect(ready.find((i) => i.key === 'product')?.detail).toMatch(/approval/i);
	});

	it('explains in plain words why an email is waiting', () => {
		expect(waitingReason('waiting: awaiting client approval: the client has not approved this email yet')).toBe('Waiting for the client to approve this email');
		expect(waitingReason('waiting: held: outside the pod\'s sending hours')).toBe('Outside the sending hours; it goes out in the next window');
		expect(waitingReason('waiting: held: pod is setup')).toBe('The pod is not live yet');
		expect(waitingReason('waiting: not signed')).toBe('Not signed by the pod\'s sender');
		expect(waitingReason(null)).toBe('');
		expect(waitingReason('something new')).toBe('something new');
	});

	it('labels client approval states and parses target-list domains', () => {
		expect(APPROVAL_BADGE.pending.label).toBe('Waiting for client');
		expect(APPROVAL_BADGE.changes_requested.tone).toBe('warn');
		expect(parseDomains('Brightvale.example, oakfield.example\nhttps://www.ridgeway.example/about  oakfield.example')).toEqual([
			'brightvale.example', 'oakfield.example', 'ridgeway.example'
		]);
	});

	it('names every document', () => {
		expect(Object.keys(DOC_LABELS)).toEqual(['icp', 'voice', 'product', 'objections', 'targets', 'exclusions', 'playbook', 'qualification', 'handoff']);
	});
});
