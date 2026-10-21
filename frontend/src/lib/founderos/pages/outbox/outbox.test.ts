import { describe, expect, it } from 'vitest';
import { approvalOf, canApprove, originLine, recipientLine } from './outbox';

describe('outbox helpers', () => {
	it('names the recipient with whatever is known', () => {
		expect(recipientLine({ to: 'info@hitratech.co.uk', toName: 'Eren Can', company: 'Hitratech' })).toBe(
			'Eren Can · Hitratech <info@hitratech.co.uk>'
		);
		expect(recipientLine({ to: 'hello@x.co', toName: null, company: ' ' })).toBe('hello@x.co');
	});

	it('shows the source task when there is one', () => {
		expect(originLine({ source: 'paperclip', sourceRef: 'THE-12' })).toBe('paperclip · THE-12');
		expect(originLine({ source: 'paperclip', sourceRef: null })).toBe('paperclip');
	});

	it('approves with exactly the reviewed content', () => {
		expect(approvalOf(' Subject ', 'Body\n')).toEqual({ subject: 'Subject', body: 'Body', force: false });
		expect(approvalOf('s', 'b', true).force).toBe(true);
	});

	it('approves only waiting or failed rows with content', () => {
		expect(canApprove({ status: 'pending' }, 's', 'b')).toBe(true);
		expect(canApprove({ status: 'failed' }, 's', 'b')).toBe(true);
		expect(canApprove({ status: 'sending' }, 's', 'b')).toBe(false);
		expect(canApprove({ status: 'pending' }, ' ', 'b')).toBe(false);
		expect(canApprove({ status: 'uncertain' }, 's', 'b')).toBe(false);
		expect(canApprove({ status: 'uncertain' }, 's', 'b', true)).toBe(true);
	});
});
