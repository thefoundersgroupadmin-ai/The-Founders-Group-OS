import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('$lib/founderos/api', () => ({ founderosFetch: vi.fn() }));

import { founderosFetch } from '$lib/founderos/api';
import AiGuard from './AiGuard.svelte';

const fetchMock = vi.mocked(founderosFetch);
const reset = '2026-11-02T10:24:44Z';

beforeEach(() => fetchMock.mockReset());

describe('AI pause controls', () => {
	it('lets the owner resume once until the weekly reset and says which agents woke', async () => {
		fetchMock.mockResolvedValueOnce({ cutoff: 50, paused: true, reason: 'Codex weekly usage reached 50%', canControl: true, usedPercent: 67, resetsAt: reset });
		render(AiGuard);
		const btn = await screen.findByRole('button', { name: /Resume until/ });
		expect(screen.getByText(/67% of the weekly limit/)).toBeTruthy();
		fetchMock.mockResolvedValueOnce({ ok: true, until: reset, resumed: ['CEO', 'Research'], kept: ['Old'], failed: [] });
		fetchMock.mockResolvedValueOnce({ cutoff: 50, paused: false, canControl: true, usedPercent: 67, resetsAt: reset, goAheadUntil: reset });
		await fireEvent.click(btn);
		expect(fetchMock).toHaveBeenCalledWith('/pages/usage/ai/resume', { method: 'POST' });
		await screen.findByText(/Woke CEO, Research/);
		expect(screen.getByText(/Old stays paused/)).toBeTruthy();
		await screen.findByRole('button', { name: 'Pause now' });
		expect(screen.getByText(/on your go-ahead until/)).toBeTruthy();
	});

	it('pauses everything when the owner presses Pause now', async () => {
		fetchMock.mockResolvedValueOnce({ cutoff: 50, paused: false, canControl: true, usedPercent: 20 });
		render(AiGuard);
		const btn = await screen.findByRole('button', { name: 'Pause now' });
		fetchMock.mockResolvedValueOnce({ ok: true, paused: ['CEO'] });
		fetchMock.mockResolvedValueOnce({ cutoff: 50, paused: true, reason: 'Paused by owner', canControl: true, usedPercent: 20 });
		await fireEvent.click(btn);
		expect(fetchMock).toHaveBeenCalledWith('/pages/usage/ai/pause', { method: 'POST' });
		await screen.findByRole('button', { name: /Resume/ });
	});

	it('shows a teammate the pause without any buttons', async () => {
		fetchMock.mockResolvedValueOnce({ cutoff: 50, paused: true, reason: 'cutoff', canControl: false, usedPercent: 67 });
		render(AiGuard);
		await screen.findByText(/Only the owner can resume/);
		expect(screen.queryByRole('button')).toBeNull();
	});

	it('reports a failed resume instead of pretending', async () => {
		fetchMock.mockResolvedValueOnce({ cutoff: 50, paused: true, reason: 'cutoff', canControl: true, usedPercent: 67, resetsAt: reset });
		render(AiGuard);
		const btn = await screen.findByRole('button', { name: /Resume until/ });
		fetchMock.mockRejectedValueOnce(new Error('could not clear the pause'));
		fetchMock.mockResolvedValueOnce({ cutoff: 50, paused: true, reason: 'cutoff', canControl: true, usedPercent: 67, resetsAt: reset });
		await fireEvent.click(btn);
		await waitFor(() => expect(screen.getByText(/could not clear the pause/)).toBeTruthy());
	});
	it('lists agents a pause left behind after the go-ahead and wakes them in one press', async () => {
		fetchMock.mockResolvedValueOnce({ cutoff: 50, paused: false, canControl: true, usedPercent: 67, goAheadUntil: reset, stillPaused: ['Prospect Research', 'Sales Agent'] });
		render(AiGuard);
		expect(await screen.findByText(/Still paused: Prospect Research, Sales Agent/)).toBeTruthy();
		expect(fetchMock).toHaveBeenCalledWith('/pages/usage/ai?agents=1');
		fetchMock.mockResolvedValueOnce({ ok: true, resumed: ['Prospect Research', 'Sales Agent'], kept: [], failed: [] });
		fetchMock.mockResolvedValueOnce({ cutoff: 50, paused: false, canControl: true, usedPercent: 67, goAheadUntil: reset });
		await fireEvent.click(screen.getByRole('button', { name: 'Wake 2 agents' }));
		expect(fetchMock).toHaveBeenCalledWith('/pages/usage/ai/wake', { method: 'POST' });
		await screen.findByText(/Woke Prospect Research, Sales Agent/);
		await waitFor(() => expect(screen.queryByRole('button', { name: /Wake/ })).toBeNull());
	});
});
