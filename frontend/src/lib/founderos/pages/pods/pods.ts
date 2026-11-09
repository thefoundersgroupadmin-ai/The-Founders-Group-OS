// Pure helpers for /os/pods (client AI SDR pods, docs/tfg/CLIENT-PODS.md).
import type { BadgeTone } from '$lib/founderos/kit';
import type { ClientApproval, Pod, PodStatus, PodMeeting, WeekMetrics, WeeklyBody } from './types';

export const STATUS_BADGE: Record<PodStatus, { tone: BadgeTone; label: string }> = {
	setup: { tone: 'default', label: 'Setting up' },
	dry_run: { tone: 'accent', label: 'Test run' },
	live: { tone: 'ok', label: 'Live' },
	paused: { tone: 'warn', label: 'Paused' },
	handed_over: { tone: 'default', label: 'Handed over' }
};

export const DOC_LABELS: Record<string, { label: string; hint: string }> = {
	icp: { label: 'Ideal customer', hint: 'Who we sell to: industry, size, roles, signals' },
	voice: { label: 'Voice', hint: 'How the client sounds; words to use and avoid' },
	product: { label: 'Product and pricing', hint: 'What we sell, for whom, what it costs' },
	objections: { label: 'Objections', hint: 'Common pushback and the honest answers' },
	targets: { label: 'Target accounts', hint: 'Named accounts and segments to work' },
	exclusions: { label: 'Never contact', hint: 'Customers, competitors, partners, do-not-email' },
	playbook: { label: 'Playbook', hint: 'Sequences, angles, proof points' },
	qualification: { label: 'Qualification', hint: 'Budget, timing, who decides: what counts as qualified' },
	handoff: { label: 'Hand-off', hint: 'When and how a person takes over' }
};

export type StatusAction = { to: PodStatus; label: string; needsReason?: boolean };

const MOVES: Record<PodStatus, StatusAction[]> = {
	setup: [{ to: 'dry_run', label: 'Start test run' }],
	dry_run: [
		{ to: 'live', label: 'Go live' },
		{ to: 'paused', label: 'Pause', needsReason: true },
		{ to: 'setup', label: 'Back to setup' }
	],
	live: [
		{ to: 'paused', label: 'Pause', needsReason: true },
		{ to: 'handed_over', label: 'Hand over' }
	],
	paused: [
		{ to: 'live', label: 'Resume' },
		{ to: 'dry_run', label: 'Back to test run' },
		{ to: 'handed_over', label: 'Hand over' }
	],
	handed_over: []
};

/** The status buttons for a pod: exactly the moves the backend allows. */
export function actionsFor(p: Pick<Pod, 'status'>): StatusAction[] {
	return MOVES[p.status];
}

export type ChecklistItem = { key: string; label: string; done: boolean; detail: string };

/** What stands between this pod and a live AI SDR, in order. */
export function setupChecklist(
	p: Pod,
	mailbox: { configured: boolean },
	docs: Record<string, { approved: boolean } | undefined>,
	agent: { agentId: string } | null
): ChecklistItem[] {
	const doc = (key: string): ChecklistItem => {
		const d = docs[key];
		return {
			key,
			label: DOC_LABELS[key].label,
			done: !!d?.approved,
			detail: !d ? 'not written yet' : d.approved ? 'approved by the client' : 'written; waiting for client approval'
		};
	};
	return [
		{ key: 'mailbox', label: 'Sending mailbox', done: mailbox.configured && !!p.senderEmail, detail: mailbox.configured ? p.senderEmail : 'not connected' },
		doc('icp'),
		doc('voice'),
		doc('product'),
		doc('targets'),
		{ key: 'agent', label: 'AI SDR on the task board', done: !!agent?.agentId, detail: agent?.agentId ? 'provisioned' : 'not provisioned' },
		{ key: 'dryrun', label: 'Test run', done: p.status !== 'setup', detail: p.status === 'setup' ? 'not started' : 'done or running' },
		{ key: 'live', label: 'Live', done: !!p.wentLiveAt, detail: p.wentLiveAt ? `since ${p.wentLiveAt.slice(0, 10)}` : 'not yet' }
	];
}

const REASONS: Array<[RegExp, string]> = [
	[/awaiting client approval: .*target list/i, 'The company is not on a target list the client approved'],
	[/awaiting client approval: .*asked for changes/i, 'The client asked for changes to this email'],
	[/awaiting client approval/i, 'Waiting for the client to approve this email'],
	[/outside the pod's sending hours/i, 'Outside the sending hours; it goes out in the next window'],
	[/cap .* is reached/i, "Today's sending limit is reached; it goes out tomorrow"],
	[/pod is (setup|paused|handed over)/i, 'The pod is not live yet'],
	[/sending or its delivery is uncertain/i, 'Another email to this person is still in flight'],
	[/no approved sink/i, 'Test run has no test inbox set'],
	[/no sending account/i, 'The pod has no sending mailbox'],
	[/not signed/i, "Not signed by the pod's sender"],
	[/no opt-out line/i, 'Missing the opt-out line'],
	[/no sender name/i, 'The pod has no sender name']
];

/** A row's waiting note in plain words. */
export function waitingReason(err: string | null): string {
	if (!err) return '';
	for (const [re, text] of REASONS) if (re.test(err)) return text;
	return err;
}

export const APPROVAL_BADGE: Record<ClientApproval['status'], { tone: BadgeTone; label: string }> = {
	pending: { tone: 'accent', label: 'Waiting for client' },
	approved: { tone: 'ok', label: 'Approved' },
	changes_requested: { tone: 'warn', label: 'Changes requested' },
	withdrawn: { tone: 'default', label: 'Withdrawn' }
};

/** Company domains from pasted text: commas, spaces, lines or URLs; lower-cased, de-duplicated, in order. */
export function parseDomains(text: string): string[] {
	const out: string[] = [];
	for (const raw of text.split(/[\s,;]+/)) {
		const host = raw.trim().toLowerCase().replace(/^https?:\/\//, '').replace(/^www\./, '').split('/')[0];
		if (host && host.includes('.') && !out.includes(host)) out.push(host);
	}
	return out;
}

/** Where a meeting is, from booking to coaching notes. */
export function meetingStage(m: PodMeeting, now = new Date()): { tone: BadgeTone; label: string } {
	if (m.status === 'cancelled') return { tone: 'default', label: 'Cancelled' };
	if (m.outcome === 'no_show') return { tone: 'warn', label: 'No-show' };
	if (m.coaching) return { tone: 'ok', label: m.coachingSent ? 'Coaching sent' : 'Coaching ready' };
	if (m.hasTranscript) return { tone: 'accent', label: 'Coaching pending' };
	const past = m.startsAt !== null && new Date(m.startsAt) < now;
	if (past) return { tone: 'warn', label: m.outcome === 'held' ? 'Needs a transcript' : 'How did it go?' };
	if (m.brief) return { tone: m.briefSent ? 'ok' : 'accent', label: m.briefSent ? 'Brief sent' : 'Brief ready' };
	return { tone: 'default', label: 'Brief pending' };
}

/** The last four recorded weeks against the client's baseline (per month ≈ 4 weeks). */
export function resultsSummary(body: WeeklyBody): { meetings: number; meetingsBefore: number | null; replyRate: number | null; replyRateBefore: number | null } {
	const last = body.weeks.slice(0, 4);
	const sum = (k: keyof WeekMetrics) => last.reduce((a, w) => a + (Number(w.metrics[k]) || 0), 0);
	const reached = sum('prospectsReached');
	const set = Boolean(body.baseline?.setAt);
	return {
		meetings: sum('meetingsBooked'),
		meetingsBefore: set ? body.baseline.meetingsPerMonth : null,
		replyRate: reached ? Math.round((sum('replies') / reached) * 1000) / 10 : null,
		replyRateBefore: set ? body.baseline.replyRatePct : null
	};
}
