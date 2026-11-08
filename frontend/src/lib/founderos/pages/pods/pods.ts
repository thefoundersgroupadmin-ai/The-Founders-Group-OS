// Pure helpers for /os/pods (client AI SDR pods, docs/tfg/CLIENT-PODS.md).
import type { BadgeTone } from '$lib/founderos/kit';
import type { Pod, PodStatus } from './types';

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
