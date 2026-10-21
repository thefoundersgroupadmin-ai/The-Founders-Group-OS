// Pure helpers for /os/outbox.
import type { BadgeTone } from '$lib/founderos/kit';
import type { OutboxItem, OutboxStatus } from './types';

export const STATUS_BADGE: Record<OutboxStatus, { tone: BadgeTone; label: string }> = {
	pending: { tone: 'accent', label: 'Waiting' },
	sending: { tone: 'warn', label: 'Sending' },
	sent: { tone: 'ok', label: 'Sent' },
	rejected: { tone: 'default', label: 'Rejected' },
	failed: { tone: 'err', label: 'Failed' },
	uncertain: { tone: 'warn', label: 'Uncertain' }
};

/** "Eren Can · Hitratech <info@hitratech.co.uk>", skipping whatever is unknown. */
export function recipientLine(it: Pick<OutboxItem, 'to' | 'toName' | 'company'>): string {
	const who = [it.toName, it.company].filter((s): s is string => !!s && s.trim() !== '').join(' · ');
	return who ? `${who} <${it.to}>` : it.to;
}

/** Where the draft came from, e.g. "paperclip · THE-12". */
export function originLine(it: Pick<OutboxItem, 'source' | 'sourceRef'>): string {
	return it.sourceRef ? `${it.source} · ${it.sourceRef}` : it.source;
}

/** The approve body: exactly what the operator is looking at, which is what gets sent. */
export function approvalOf(subject: string, body: string, force = false): { subject: string; body: string; force: boolean } {
	return { subject: subject.trim(), body: body.trim(), force };
}

/** Approve is allowed for a waiting or failed row with a non-blank subject and body. An
 *  uncertain row may have been delivered, so it needs the explicit "retry anyway". */
export function canApprove(it: Pick<OutboxItem, 'status'>, subject: string, body: string, force = false): boolean {
	const open = it.status === 'pending' || it.status === 'failed' || (force && it.status === 'uncertain');
	return open && subject.trim() !== '' && body.trim() !== '';
}
