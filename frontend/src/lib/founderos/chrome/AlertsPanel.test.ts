import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AlertStatus } from './alerts';

vi.mock('$lib/founderos/api', () => ({ founderosFetch: vi.fn() }));
vi.mock('$lib/founderos/voice', () => ({ speak: vi.fn(() => Promise.resolve()), stopSpeaking: vi.fn() }));

import { founderosFetch } from '$lib/founderos/api';
import { speak } from '$lib/founderos/voice';
import AlertsPanel from './AlertsPanel.svelte';

const fetchMock = vi.mocked(founderosFetch);
const status = (over: Partial<AlertStatus> = {}): AlertStatus => ({
	publicKey: 'AQID',
	devices: [],
	kinds: [
		{ kind: 'approval', label: 'An agent needs your decision', on: true },
		{ kind: 'sent', label: 'A follow-up went out', on: true }
	],
	briefHour: 8,
	voice: { natural: true, detail: 'natural voice (ElevenLabs)' },
	...over
});

const realUA = navigator.userAgent;
function setUA(ua: string) {
	Object.defineProperty(navigator, 'userAgent', { value: ua, configurable: true });
}

beforeEach(() => fetchMock.mockReset());
afterEach(() => setUA(realUA));

describe('the alerts drawer', () => {
	it('tells an iPhone in Safari to add the OS to the home screen first', async () => {
		setUA('Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1');
		fetchMock.mockResolvedValue(status());
		render(AlertsPanel);
		await screen.findByText(/Add to Home Screen/);
		expect(screen.queryByRole('button', { name: 'Turn on alerts here' })).toBeNull();
	});

	it('switches one kind off and sets the brief hour', async () => {
		fetchMock.mockResolvedValue(status());
		render(AlertsPanel);
		const box = (await screen.findByLabelText('A follow-up went out')) as HTMLInputElement;
		await fireEvent.click(box);
		expect(fetchMock).toHaveBeenCalledWith('/pages/notify/prefs', { method: 'PUT', json: { kinds: { sent: false } } });
		await fireEvent.change(screen.getByRole('combobox', { name: 'Morning brief hour' }), { target: { value: '7' } });
		expect(fetchMock).toHaveBeenCalledWith('/pages/notify/prefs', { method: 'PUT', json: { briefHour: 7 } });
	});

	it("plays today's brief in the OS voice", async () => {
		fetchMock.mockImplementation(async (path: string) =>
			(path === '/pages/notify/brief' ? { text: 'Good morning. You have 2 calls today.' } : path === '/pages/notify' ? status() : { voices: [], current: '' }) as never
		);
		render(AlertsPanel);
		await fireEvent.click(await screen.findByRole('button', { name: /Play today's brief/ }));
		await waitFor(() => expect(speak).toHaveBeenCalledWith('Good morning. You have 2 calls today.'));
	});
});
