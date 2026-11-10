import { describe, expect, it } from 'vitest';
import { repairHeadline, repairRows } from './repair';

const state = {
	lastPass: '2026-11-10T18:00:00Z',
	checks: {
		site: { label: 'Website', status: 'ok', detail: '200' },
		portal: { label: 'Client portal database', status: 'needs_owner', detail: 'blocked', ownerAction: 'Upgrade Supabase' },
		web: { label: 'OS web app', status: 'repairing', detail: 'down', note: 'AI repair in progress' }
	}
};

describe('auto-repair rows', () => {
	it('puts what needs the owner first and shows their action', () => {
		const rows = repairRows(state);
		expect(rows.map((r) => r.id)).toEqual(['portal', 'web', 'site']);
		expect(rows[0]).toMatchObject({ tone: 'err', action: 'Upgrade Supabase' });
		expect(rows[1]).toMatchObject({ tone: 'warn', action: 'AI repair in progress' });
		expect(rows[2].action).toBeNull();
	});
	it('summarises', () => {
		expect(repairHeadline(state)).toEqual({ text: '1 needs you · 1 being fixed', tone: 'err' });
		expect(repairHeadline({ lastPass: 'x', checks: { a: { status: 'ok' } } })).toEqual({ text: 'all 1 checks healthy', tone: 'ok' });
		expect(repairHeadline(null).tone).toBe('warn');
	});
});
