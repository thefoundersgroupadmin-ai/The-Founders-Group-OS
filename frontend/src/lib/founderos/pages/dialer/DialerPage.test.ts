import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { DialerBoard, Prospect } from './dialer';
import { clock, nextMorning, prettyPhone } from './dialer';

vi.mock('$lib/founderos/api', () => ({ founderosFetch: vi.fn() }));
vi.mock('./phone', () => ({ dial: vi.fn(), shutdown: vi.fn() }));

import { founderosFetch } from '$lib/founderos/api';
import { dial } from './phone';
import DialerPage from './DialerPage.svelte';

const fetchMock = vi.mocked(founderosFetch);
const dialMock = vi.mocked(dial);

const board = (over: Partial<DialerBoard> = {}): DialerBoard => ({
	twilio: { configured: true, ready: true, callerId: '+15005550006' },
	apollo: { configured: true },
	queue: [],
	stats: { dials: 3, connects: 1, meetings: 0, talkSeconds: 95 },
	contacts: { withPhone: 4, noPhone: 6 },
	...over
});
const dana: Prospect = {
	contactId: 'fc-dana', name: 'Dana Reyes', person: 'Dana Reyes', company: 'Reyes Co', role: 'CEO', email: 'dana@reyes.co',
	phone: '+12127365000', localTime: '11:00 Thu', zone: 'America/New_York', status: 'first_touch', likelihood: 80, attempts: 0,
	callbackDue: false, brief: 'WHO: Dana Reyes, CEO, Reyes Co\nWHY NOW: hiring a first AE\nOPENER: Saw you are hiring your first AE…'
};

beforeEach(() => {
	fetchMock.mockReset();
	dialMock.mockReset();
});

describe('dialer helpers', () => {
	it('reads numbers and times the way a caller does', () => {
		expect(prettyPhone('+12127365000')).toBe('(212) 736-5000');
		expect(prettyPhone('+447700900123')).toBe('+447700900123');
		expect(clock(95)).toBe('1:35');
		expect(nextMorning(new Date(2026, 9, 23, 15, 0))).toBe('2026-10-26T10:00'); // Friday → Monday
	});
});

describe('the dialer', () => {
	it('walks setup until Twilio is connected', async () => {
		fetchMock.mockResolvedValue(board({ twilio: { configured: false, ready: false, callerId: '' } }));
		render(DialerPage);
		await screen.findByText(/paste the Account SID and Auth Token/);
		expect((screen.getByRole('button', { name: /Finish setup/ }) as HTMLButtonElement).disabled).toBe(true);
	});

	it('takes the next prospect, calls them, then a key records the outcome and drafts the follow-up', async () => {
		const calls: string[] = [];
		fetchMock.mockImplementation(async (path: string, init?: { method?: string; json?: unknown }) => {
			calls.push(`${init?.method ?? 'GET'} ${path}${init?.json ? ' ' + JSON.stringify(init.json) : ''}`);
			if (path === '/pages/dialer') return board() as never;
			if (path === '/pages/dialer/next') return { prospect: dana } as never;
			if (path === '/pages/dialer/calls') return { call: { id: 'call-1', contactId: 'fc-dana', to: dana.phone, status: 'dialing', startedAt: '' } } as never;
			return {} as never;
		});
		let push: (c: { state: string }) => void = () => {};
		dialMock.mockImplementation(async (_id, onChange) => {
			const live = { state: 'connecting', muted: false, hangUp: () => onChange({ ...live, state: 'ended' } as never), mute: vi.fn(), digits: vi.fn() };
			push = (c) => onChange({ ...live, ...c } as never);
			return live as never;
		});
		render(DialerPage);
		await fireEvent.click(await screen.findByRole('button', { name: 'Start dialling' }));
		await screen.findByText(/WHY NOW: hiring a first AE/);
		expect(screen.getByText('(212) 736-5000')).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: /Call \(space\)/ }));
		await waitFor(() => expect(dialMock).toHaveBeenCalledWith('call-1', expect.any(Function), expect.any(Function)));
		push({ state: 'open' });
		await screen.findByText(/on the call/);
		await fireEvent.input(screen.getByLabelText('Call notes'), { target: { value: 'Booked Tue 2pm' } });
		await fireEvent.click(screen.getByRole('button', { name: /Hang up/ }));
		await screen.findByText(/How did it go/);

		await fireEvent.keyDown(window, { key: '1' });
		await waitFor(() => expect(calls.some((c) => c.startsWith('POST /pages/dialer/calls/call-1/outcome') && c.includes('"outcome":"meeting"') && c.includes('Booked Tue 2pm'))).toBe(true));
		await waitFor(() => expect(calls).toContain('POST /pages/dialer/calls/call-1/followup {}'));
		// straight on to the next one
		await waitFor(() => expect(calls.filter((c) => c.startsWith('POST /pages/dialer/next')).length).toBe(2));
	});

	it('says plainly when nobody can be called', async () => {
		fetchMock.mockImplementation(async (path: string) =>
			(path === '/pages/dialer/next' ? { prospect: null, why: 'nobody to call right now: everyone is outside their hours' } : board()) as never
		);
		render(DialerPage);
		await fireEvent.click(await screen.findByRole('button', { name: 'Start dialling' }));
		await screen.findByText(/everyone is outside their hours/);
	});
});
