// When the CEO seat can't answer, the dock says why and offers a retry,
// instead of a shimmer that waits forever.
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';

vi.mock('../api', async (orig) => ({ ...(await orig<typeof import('../api')>()), founderosFetch: vi.fn() }));

import { founderosFetch } from '../api';
import ConductorPanel from './ConductorPanel.svelte';
import { CONDUCTOR_OPEN_EVENT } from './conductor';

const fetchMock = vi.mocked(founderosFetch);

describe('the Conductor when nobody can answer', () => {
	it('shows the reason and wakes the CEO on Try again', async () => {
		const problem = "Node v22.23.3 does not satisfy Codex ACP's Node >=24.11.0 prerequisite.";
		fetchMock.mockImplementation(async (path: string, init?: { method?: string }) => {
			if (init?.method === 'POST') return { retried: true } as never;
			if (path.startsWith('/pages/conductor/context')) return { title: 'Home', context: '', quickActions: [], model: null } as never;
			if (path === '/pages/conductor/ask') return { turns: [] } as never;
			return { messages: [], health: { ok: false, agent: 'CEO', problem } } as never;
		});
		render(ConductorPanel, { pathname: '/os' });
		window.dispatchEvent(new CustomEvent(CONDUCTOR_OPEN_EVENT));
		await tick();
		await screen.findByText("CEO agent can't take work right now");
		expect(screen.getByText(problem)).toBeTruthy();
		await fireEvent.click(screen.getByRole('button', { name: 'Try again now' }));
		expect(fetchMock).toHaveBeenCalledWith('/pages/conductor/retry', { method: 'POST', json: {} });
		await waitFor(() => expect(screen.getByText(/Woken/)).toBeTruthy());
	});
});
