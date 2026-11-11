import { describe, expect, it } from 'vitest';
import { APPROVAL_BADGE, DOC_LABELS, STATUS_BADGE, actionsFor, meetingStage, parseDomains, resultsSummary, setupChecklist, waitingReason } from './pods';
import type { Pod, PodMeeting, WeekMetrics, WeeklyBody } from './types';

const pod = (over: Partial<Pod> = {}): Pod => ({
	slug: 'client-brightvale', clientName: 'Brightvale', package: 'core', reps: 6, status: 'setup',
	senderName: '', senderEmail: '', sendingDomain: '', approvalPolicy: 'client_approves_each',
	firstEmailDailyCap: 20, followUpDailyCap: 40, timezone: 'Europe/London', sendWindow: [9, 17], sendWeekends: false, dryRunInbox: '',
	crmProvider: '', calendarProvider: '', bookingUrl: '', pausedReason: '', portalClientId: '', handoffEmail: '', inboundAutoReply: false, inboundLinkSet: false, wentLiveAt: null, ...over
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
		expect(empty.filter((i) => !i.optional).map((i) => i.key)).toEqual(['mailbox', 'icp', 'voice', 'product', 'targets', 'agent', 'portal', 'handoff', 'calendar', 'baseline', 'dryrun', 'live']);
		expect(empty.filter((i) => i.optional).map((i) => i.key)).toEqual(['crm', 'form', 'recorder']);
		expect(empty.filter((i) => i.waivable).map((i) => i.key)).toEqual(['product', 'agent', 'portal', 'handoff', 'calendar', 'baseline']);
		const ready = setupChecklist(
			pod({ status: 'live', senderEmail: 'sam@bv.example', wentLiveAt: '2026-11-09T10:00:00Z' }),
			{ configured: true },
			{ icp: { approved: true }, voice: { approved: true }, product: { approved: false }, targets: { approved: true } },
			{ agentId: 'a1' }
		);
		expect(ready.filter((i) => !i.done && !i.optional).map((i) => i.key)).toEqual(['product', 'portal', 'handoff', 'calendar', 'baseline']);
		// A requirement waived for this client counts as done, with the reason shown.
		const waived = setupChecklist(pod({ readinessWaivers: { calendar: { reason: 'rep books from their own calendar', by: 'Nader', at: '2026-11-11' } } }),
			{ configured: true }, {}, null);
		const cal = waived.find((i) => i.key === 'calendar');
		expect(cal?.done && cal.waived && cal.detail).toMatch(/not needed: rep books/);
		const full = setupChecklist(pod({ portalClientId: 'c1', handoffEmail: 'rep@bv.example', inboundLinkSet: true }), { configured: true }, {}, null,
			{ calendar: true, recorder: true, crm: true, baseline: true });
		expect(full.filter((i) => (i.optional || ['portal', 'handoff', 'calendar', 'baseline'].includes(i.key)) && !i.done)).toEqual([]);
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

describe('meetings and results (M6)', () => {
	const m = (over: Partial<PodMeeting> = {}): PodMeeting => ({
		id: 1, uid: 'b-1', title: 'Discovery', name: 'Priya', email: 'p@x.example', startsAt: '2026-11-20T10:00:00Z', brief: '', briefSent: false,
		status: 'booked', outcome: '', hasTranscript: false, coaching: '', coachingSent: false, ...over
	});
	const before = new Date('2026-11-19T00:00:00Z');
	const after = new Date('2026-11-21T00:00:00Z');
	it('follows a meeting from brief to coaching', () => {
		expect(meetingStage(m(), before).label).toBe('Brief pending');
		expect(meetingStage(m({ brief: 'x', briefSent: true }), before).label).toBe('Brief sent');
		expect(meetingStage(m({ brief: 'x' }), after).label).toBe('How did it go?');
		expect(meetingStage(m({ outcome: 'held' }), after).label).toBe('Needs a transcript');
		expect(meetingStage(m({ outcome: 'held', hasTranscript: true }), after).label).toBe('Coaching pending');
		expect(meetingStage(m({ outcome: 'held', hasTranscript: true, coaching: 'c', coachingSent: true }), after).label).toBe('Coaching sent');
		expect(meetingStage(m({ outcome: 'no_show' }), after).label).toBe('No-show');
		expect(meetingStage(m({ status: 'cancelled' }), after).label).toBe('Cancelled');
		expect(meetingStage(m({ brief: 'x', briefDelivery: 'uncertain' }), before).label).toBe('Brief email uncertain: check the inbox');
		expect(meetingStage(m({ coaching: 'c', coachingDelivery: 'failed' }), after).label).toBe('Coaching failed to email');
	});
	it('sums the four calendar weeks before this one against the baseline', () => {
		const w = (week: string, booked: number, rate: number, cohort: number) => ({ week, review: '', metrics: { meetingsBooked: booked, replyRatePct: rate, cohortSize: cohort } as WeekMetrics });
		const now = new Date('2026-11-25T12:00:00Z'); // Wednesday; this week starts 23 Nov
		const body: WeeklyBody = {
			weeks: [w('2026-11-23', 7, 9, 9), w('2026-11-16', 2, 5, 40), w('2026-11-09', 1, 4, 30), w('2026-10-26', 1, 0, 0), w('2026-10-19', 9, 9, 9)],
			baseline: { meetingsPerMonth: 3, replyRatePct: 1.5, note: '', setAt: 'x' }, thisWeek: {} as WeekMetrics
		};
		expect(resultsSummary(body, now)).toEqual({ meetings: 4, meetingsBefore: 3, replyRate: 5, replyRateBefore: 1.5, weeksCovered: 3 });
		expect(resultsSummary({ ...body, baseline: { meetingsPerMonth: 0, replyRatePct: 0, note: '' } }, now).meetingsBefore).toBeNull();
		expect(resultsSummary({ ...body, weeks: [w('2026-08-03', 5, 5, 5)] }, now)).toMatchObject({ meetings: 0, replyRate: null, weeksCovered: 0 });
	});
});

describe('stuckTaskOf', () => {
	it('finds the board task of a stuck-task alert only', async () => {
		const { stuckTaskOf } = await import('./pods');
		expect(stuckTaskOf('task:abc-123')).toBe('abc-123');
		expect(stuckTaskOf('health:calendar')).toBe('');
	});
});
