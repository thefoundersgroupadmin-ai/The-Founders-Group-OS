import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Row, SignalsBoard } from './signals';
import { ago, heat, matches } from './signals';

vi.mock('$lib/founderos/api', () => ({ founderosFetch: vi.fn() }));

import { founderosFetch } from '$lib/founderos/api';
import SignalsPage from './SignalsPage.svelte';

const fetchMock = vi.mocked(founderosFetch);

const acme: Row = {
	contactId: 'fc-acme', person: 'Sam Lee', company: 'Acme', role: 'CEO', status: 'first_touch', website: 'https://acme.example', email: '', phone: '',
	linkedin: 'https://www.linkedin.com/in/samlee', score: 65, whyNow: 'Raised $4M Seed round; Hiring: Founding AE', checkedAt: '2026-11-12T10:00:00Z',
	signals: [
		{ kind: 'funding', title: 'Raised $4M Seed round', weight: 35, url: 'https://news.example/acme' },
		{ kind: 'hiring', title: 'Hiring: Founding AE', detail: 'sales leader role on Ashby', weight: 30 }
	],
	plays: { next: 'linkedin', nextLabel: 'Connect on LinkedIn', linkedinNote: 'Hi Sam, congrats on the $4M Seed.', emailOpener: 'Hi Sam,\n\nCongrats', callOpener: 'Hi Sam', offer: 'Sales System Build' }
};
const beta: Row = { ...acme, contactId: 'fc-beta', company: 'Beta', person: '', score: 0, whyNow: '', checkedAt: null, signals: [], plays: { next: 'research', nextLabel: 'Find the buyer' } };

const board = (over: Partial<SignalsBoard> = {}): SignalsBoard => ({
	rows: [acme, beta],
	stats: { Open: 2, Checked: 1, WithSignals: 1, Hot: 1, Hiring: 1, Funded: 1, NoEmail: 2 },
	lusha: { configured: false, usedToday: 0, dailyCap: 20 },
	outreachPaused: true,
	autoChecks: true,
	...over
});

beforeEach(() => fetchMock.mockReset());

describe('signals helpers', () => {
	it('grades and filters', () => {
		expect(heat(65)).toBe('hot');
		expect(heat(20)).toBe('warm');
		expect(heat(0)).toBe('cool');
		expect(matches(acme, 'hiring')).toBe(true);
		expect(matches(beta, 'unchecked')).toBe(true);
		expect(matches(beta, 'hot')).toBe(false);
		expect(ago(null)).toBe('not checked yet');
		expect(ago('2026-11-12T10:00:00Z', new Date('2026-11-13T10:00:00Z'))).toBe('checked 1d ago');
	});
});

describe('SignalsPage', () => {
	it('shows the strongest prospect with its evidence and openers, and says what is off', async () => {
		fetchMock.mockResolvedValueOnce(board());
		render(SignalsPage);
		await screen.findByText('Hiring: Founding AE');
		expect(screen.getByText('Raised $4M Seed round').closest('a')?.getAttribute('href')).toBe('https://news.example/acme');
		expect(screen.getByText('Hi Sam, congrats on the $4M Seed.')).toBeTruthy();
		expect(screen.getByText(/Lusha not connected/)).toBeTruthy();
		expect(screen.getByText(/Automatic outreach is paused/)).toBeTruthy();
	});

	it('records a LinkedIn touch and filters the list', async () => {
		fetchMock.mockResolvedValueOnce(board());
		render(SignalsPage);
		await screen.findByText('I sent it');
		fetchMock.mockResolvedValueOnce({ ok: true }).mockResolvedValueOnce(board());
		await fireEvent.click(screen.getByText('I sent it'));
		await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/pages/signals/touched', { method: 'POST', json: { contactId: 'fc-acme', channel: 'linkedin' } }));
		await fireEvent.click(screen.getByText('Not checked'));
		expect(screen.getByText('1 of 2, strongest reason first')).toBeTruthy();
	});

	it('reports a Lusha that is not connected without failing', async () => {
		fetchMock.mockResolvedValueOnce(board());
		render(SignalsPage);
		await screen.findByText('Find email and phone (Lusha)');
		fetchMock.mockResolvedValueOnce({ state: 'not_configured' });
		await fireEvent.click(screen.getByText('Find email and phone (Lusha)'));
		await screen.findByText(/add the API key in Connections/);
	});

	it('says when the board cannot load', async () => {
		fetchMock.mockRejectedValueOnce(new Error('offline'));
		render(SignalsPage);
		await screen.findByText('Signals unavailable: offline');
	});
});
