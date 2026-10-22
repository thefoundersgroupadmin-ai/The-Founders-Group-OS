import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { VoiceList } from './alerts';

vi.mock('$lib/founderos/api', () => ({ founderosFetch: vi.fn() }));
vi.mock('$lib/founderos/voice', () => ({ speak: vi.fn(() => Promise.resolve()), stopSpeaking: vi.fn() }));

import { founderosFetch } from '$lib/founderos/api';
import { speak } from '$lib/founderos/voice';
import VoicePicker from './VoicePicker.svelte';

const fetchMock = vi.mocked(founderosFetch);
const v = (id: string, name: string, accent: string, gender: string) => ({ id, name, description: `${name} desc`, accent, gender, age: 'middle aged', preview: `https://cdn/${id}.mp3` });
const LIST: VoiceList = {
	voices: [v('sarah', 'Sarah', 'american', 'female'), v('lily', 'Lily', 'british', 'female'), v('brian', 'Brian', 'american', 'male')],
	current: 'brian'
};

beforeEach(() => fetchMock.mockReset());

describe('the voice picker', () => {
	it('opens on American female voices and switches with one tap, then says hello in the new voice', async () => {
		fetchMock.mockResolvedValueOnce(LIST);
		render(VoicePicker);
		await screen.findByText('Sarah');
		expect(screen.queryByText('Lily')).toBeNull();
		expect(screen.queryByText('Brian desc')).toBeNull();
		fetchMock.mockResolvedValueOnce({ ...LIST, current: 'sarah' });
		await fireEvent.click(screen.getByRole('button', { name: 'use' }));
		expect(fetchMock).toHaveBeenCalledWith('/pages/voice', { method: 'PUT', json: { voiceId: 'sarah' } });
		await waitFor(() => expect(speak).toHaveBeenCalledWith("Hi, I'm Sarah. This is how the OS will sound now."));
		await screen.findByText('in use');
	});
});
