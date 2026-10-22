import { describe, expect, it } from 'vitest';
import { laptopLine, loginLine } from './accounts';

describe('my AI accounts lines', () => {
	it('says whose plan a login runs on, or that AI stays off without one', () => {
		expect(loginLine({ connected: true, via: 'your Claude plan' })).toBe('connected · runs on your Claude plan');
		expect(loginLine({ connected: false, via: '' })).toMatch(/AI buttons stay off/);
	});

	it('reads a laptop as reporting, waiting for its first reading, or not connected', () => {
		const now = Date.parse('2026-10-22T12:00:00Z');
		expect(laptopLine({ connected: true, issued: true, device: 'laptop-k', lastPushAt: '2026-10-22T11:53:00Z' }, now)).toBe('reporting · last reading 7m ago');
		expect(laptopLine({ connected: false, issued: true, device: 'laptop-k', lastPushAt: null }, now)).toMatch(/waiting for the first reading/);
		expect(laptopLine({ connected: false, issued: false, device: 'laptop-k', lastPushAt: null }, now)).toBe('not connected');
	});
});
