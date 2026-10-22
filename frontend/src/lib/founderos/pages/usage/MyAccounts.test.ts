import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AiAccounts } from './accounts';

vi.mock('$lib/founderos/api', () => ({ founderosFetch: vi.fn() }));

import { founderosFetch } from '$lib/founderos/api';
import MyAccounts from './MyAccounts.svelte';

const fetchMock = vi.mocked(founderosFetch);

const mate = (over: Partial<AiAccounts> = {}): AiAccounts => ({
	owner: false,
	claude: { connected: false, via: '' },
	codex: { connected: false, via: '', login: { state: 'none' } },
	laptop: { connected: false, issued: false, device: 'laptop-karim', lastPushAt: null },
	...over
});

beforeEach(() => fetchMock.mockReset());

describe('My AI accounts', () => {
	it('walks a teammate through their own Claude token and saves it without showing it back', async () => {
		fetchMock.mockResolvedValue(mate());
		render(MyAccounts);
		await screen.findByText(/claude setup-token/);
		const input = screen.getByLabelText('Claude token') as HTMLInputElement;
		expect(input.type).toBe('password');
		await fireEvent.input(input, { target: { value: 'sk-ant-oat01-abcdefghijklmnopqrstuvwxyz' } });
		fetchMock.mockResolvedValue(mate({ claude: { connected: true, via: 'your Claude plan' } }));
		await fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0]);
		expect(fetchMock).toHaveBeenCalledWith('/pages/ai-accounts/claude', { method: 'PUT', json: { token: 'sk-ant-oat01-abcdefghijklmnopqrstuvwxyz' } });
		await screen.findByText('connected · runs on your Claude plan');
	});

	it('shows the ChatGPT sign-in code once codex prints it', async () => {
		fetchMock.mockResolvedValue(mate({ codex: { connected: false, via: '', login: { state: 'waiting', link: 'https://auth.openai.com/codex/device', code: 'AB12-CD34' } } }));
		render(MyAccounts);
		await screen.findByText('AB12-CD34');
	});

	it('gives a copyable one-line laptop install', async () => {
		fetchMock.mockResolvedValue(mate());
		render(MyAccounts);
		const btn = await screen.findByRole('button', { name: 'Connect this laptop' });
		fetchMock.mockResolvedValueOnce({ command: 'curl -fsSL https://os/x | TFG_LAPTOP_TOKEN=tfgl_1 bash', device: 'laptop-karim' });
		await fireEvent.click(btn);
		await waitFor(() => expect(screen.getByText(/TFG_LAPTOP_TOKEN=tfgl_1/)).toBeTruthy());
	});

	it('tells the owner they run on this Mac and asks for nothing', async () => {
		fetchMock.mockResolvedValue(mate({ owner: true, claude: { connected: true, via: "this Mac's Claude login" }, codex: { connected: true, via: "this Mac's Codex login", login: { state: 'none' } } }));
		render(MyAccounts);
		await screen.findByText(/You are the OS owner/);
		expect(screen.queryByLabelText('Claude token')).toBeNull();
	});
});
