// Pure helpers for /os/approvals.
import type { ApprovalItem, ApprovalQuestion, VerdictValue } from './types';

/** Button labels, falling back to plain words when the agent gave none. */
export function labelsOf(it: Pick<ApprovalItem, 'acceptLabel' | 'rejectLabel'>): { accept: string; reject: string } {
	return {
		accept: it.acceptLabel.trim() || 'Approve',
		reject: it.rejectLabel.trim() || 'Send back'
	};
}

/** A send-back may need a reason (the agent asked for one) and is capped at 4000. */
export function canReject(it: Pick<ApprovalItem, 'rejectRequiresReason'>, reason: string): boolean {
	const r = reason.trim();
	return r.length <= 4000 && (!it.rejectRequiresReason || r.length > 0);
}

/** Toggle one option: single-select questions keep only the newest choice. */
export function choose(q: Pick<ApprovalQuestion, 'selectionMode'>, current: string[], optionId: string): string[] {
	if (q.selectionMode !== 'multi') return [optionId];
	return current.includes(optionId) ? current.filter((id) => id !== optionId) : [...current, optionId];
}

export type AnswerBody = {
	questionId: string;
	optionIds: string[];
	otherText?: string;
};

/** The respond body; null until every required question is answered. A
 *  free-text option counts only with text typed; an allowOther question can
 *  be answered with typed text alone; optional questions with no choice are
 *  left out. */
export function answersOf(
	questions: Pick<ApprovalQuestion, 'id' | 'required' | 'options' | 'allowOther'>[],
	picked: Record<string, string[]>,
	texts: Record<string, string> = {}
): AnswerBody[] | null {
	const out: AnswerBody[] = [];
	for (const q of questions) {
		const ids = picked[q.id] ?? [];
		const text = (texts[q.id] ?? '').trim();
		const needsText = q.options.some((o) => o.freeText && ids.includes(o.id));
		if (needsText && !text) return null;
		const other = q.allowOther && !needsText && text !== '';
		if (ids.length === 0 && !other) {
			if (q.required) return null;
			continue;
		}
		out.push(needsText || other ? { questionId: q.id, optionIds: ids, otherText: text } : { questionId: q.id, optionIds: ids });
	}
	return out;
}

/** Ticked boxes must sit within the agent's min/max. */
export function checkboxesValid(it: Pick<ApprovalItem, 'minSelected' | 'maxSelected'>, selected: string[]): boolean {
	return selected.length >= it.minSelected && (it.maxSelected === null || selected.length <= it.maxSelected);
}

/** Toggle a checkbox or suggested-task key. */
export function toggleIn(current: string[], id: string): string[] {
	return current.includes(id) ? current.filter((x) => x !== id) : [...current, id];
}

/** The verdicts body for the items still open; null until each has a verdict and every required reason is given. */
export function verdictsOf(
	it: Pick<ApprovalItem, 'items' | 'requireReasonOn'>,
	chosen: Record<string, VerdictValue>,
	reasons: Record<string, string>
): { id: string; verdict: VerdictValue; reason?: string }[] | null {
	const out: { id: string; verdict: VerdictValue; reason?: string }[] = [];
	for (const item of it.items) {
		// Already judged on the board: the recorded verdict stands, so do not resend it.
		if (item.resolved) continue;
		const verdict = chosen[item.id];
		const reason = (reasons[item.id] ?? '').trim();
		if (!verdict || (it.requireReasonOn.includes(verdict) && !reason)) return null;
		out.push(reason ? { id: item.id, verdict, reason } : { id: item.id, verdict });
	}
	return out;
}
