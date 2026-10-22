import { describe, expect, it } from 'vitest';
import { answersOf, canReject, checkboxesValid, choose, labelsOf, toggleIn, verdictsOf } from './approvals';

describe('approvals helpers', () => {
	it('uses the agent labels, or plain fallbacks', () => {
		expect(
			labelsOf({
				acceptLabel: 'Approve completion',
				rejectLabel: 'Continue work'
			})
		).toEqual({ accept: 'Approve completion', reject: 'Continue work' });
		expect(labelsOf({ acceptLabel: ' ', rejectLabel: '' })).toEqual({
			accept: 'Approve',
			reject: 'Send back'
		});
	});

	it('requires a reason only when the agent asked for one', () => {
		expect(canReject({ rejectRequiresReason: true }, '  ')).toBe(false);
		expect(canReject({ rejectRequiresReason: true }, 'add sources')).toBe(true);
		expect(canReject({ rejectRequiresReason: false }, '')).toBe(true);
		expect(canReject({ rejectRequiresReason: false }, 'x'.repeat(4001))).toBe(false);
	});

	it('single choice replaces, multi choice toggles', () => {
		expect(choose({ selectionMode: 'single' }, ['us'], 'uk')).toEqual(['uk']);
		expect(choose({ selectionMode: 'multi' }, ['us'], 'uk')).toEqual(['us', 'uk']);
		expect(choose({ selectionMode: 'multi' }, ['us', 'uk'], 'us')).toEqual(['uk']);
	});

	it('answers once every required question is chosen; optional ones may be skipped', () => {
		const opt = (id: string, freeText = false) => ({
			id,
			label: id,
			description: null,
			freeText
		});
		const qs = [
			{ id: 'q1', required: true, allowOther: false, options: [opt('a'), opt('desc', true)] },
			{ id: 'q2', required: false, allowOther: false, options: [opt('b')] }
		];
		expect(answersOf(qs, {})).toBeNull();
		expect(answersOf(qs, { q1: ['a'] })).toEqual([{ questionId: 'q1', optionIds: ['a'] }]);
		expect(answersOf(qs, { q1: ['desc'] }, {})).toBeNull();
		expect(answersOf(qs, { q1: ['desc'], q2: ['b'] }, { q1: ' fintech ' })).toEqual([
			{ questionId: 'q1', optionIds: ['desc'], otherText: 'fintech' },
			{ questionId: 'q2', optionIds: ['b'] }
		]);
	});

	it('keeps checkbox counts within the agent limits', () => {
		expect(checkboxesValid({ minSelected: 1, maxSelected: 2 }, [])).toBe(false);
		expect(checkboxesValid({ minSelected: 1, maxSelected: 2 }, ['a', 'b'])).toBe(true);
		expect(checkboxesValid({ minSelected: 0, maxSelected: 1 }, ['a', 'b'])).toBe(false);
		expect(checkboxesValid({ minSelected: 0, maxSelected: null }, ['a', 'b', 'c'])).toBe(true);
		expect(toggleIn(['a'], 'a')).toEqual([]);
		expect(toggleIn(['a'], 'b')).toEqual(['a', 'b']);
	});

	it('needs a verdict on every item and a reason where required', () => {
		const it0 = {
			items: [
				{ id: 'd1', label: 'A', description: null, preview: '', resolved: null, resolvedReason: null },
				{ id: 'd2', label: 'B', description: null, preview: '', resolved: null, resolvedReason: null }
			],
			requireReasonOn: ['reject' as const]
		};
		expect(verdictsOf(it0, { d1: 'approve' }, {})).toBeNull();
		expect(verdictsOf(it0, { d1: 'approve', d2: 'reject' }, {})).toBeNull();
		expect(verdictsOf(it0, { d1: 'approve', d2: 'reject' }, { d2: 'off-brand' })).toEqual([
			{ id: 'd1', verdict: 'approve' },
			{ id: 'd2', verdict: 'reject', reason: 'off-brand' }
		]);
	});

	it('lets an "other" question be answered with text alone', () => {
		const qs = [{ id: 'q1', required: true, allowOther: true, options: [{ id: 'a', label: 'A', description: null, freeText: false }] }];
		expect(answersOf(qs, {}, {})).toBeNull();
		expect(answersOf(qs, {}, { q1: 'Fintech first' })).toEqual([{ questionId: 'q1', optionIds: [], otherText: 'Fintech first' }]);
	});

	it('skips items the board already judged', () => {
		const it0 = {
			items: [
				{ id: 'd1', label: 'A', description: null, preview: '', resolved: null, resolvedReason: null },
				{ id: 'd2', label: 'B', description: null, preview: '', resolved: 'reject' as const, resolvedReason: 'off-brand' }
			],
			requireReasonOn: [] as ('approve' | 'reject' | 'defer')[]
		};
		expect(verdictsOf(it0, { d1: 'approve' }, {})).toEqual([{ id: 'd1', verdict: 'approve' }]);
	});
});
