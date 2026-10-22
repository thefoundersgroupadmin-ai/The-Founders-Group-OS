// Hands-free talk: speak, pause, it is written down on the OS Mac and sent;
// the answer is read aloud; it listens again. No browser speech recognition
// needed (the desktop app and the iPhone app have none that works).
import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

vi.mock('$lib/founderos/voice', () => ({ speak: vi.fn(() => Promise.resolve()), stopSpeaking: vi.fn(), unlockAudio: vi.fn() }));
vi.mock('$lib/founderos/listen', () => ({
	recordTurn: vi.fn(),
	transcribe: vi.fn(),
	listenOnce: vi.fn(),
	releaseMic: vi.fn(),
	needsServerListening: vi.fn(() => true)
}));

import { recordTurn, releaseMic, transcribe } from '$lib/founderos/listen';
import { speak, unlockAudio } from '$lib/founderos/voice';
import ConductorComposer from './ConductorComposer.svelte';

const flush = () => new Promise((r) => setTimeout(r, 0));

describe('hands-free talk', () => {
	it('is offered only where there is a reply to read', () => {
		render(ConductorComposer, { onSend: vi.fn() });
		expect(screen.queryByRole('button', { name: 'Talk hands-free' })).toBeNull();
	});

	it('keeps listening through silence, sends what was said, reads the answer, then listens again', async () => {
		const audio = new Blob(['x'], { type: 'audio/webm' });
		let nextTurn: (b: Blob | null) => void = () => {};
		vi.mocked(recordTurn)
			.mockResolvedValueOnce(null) // nobody spoke yet
			.mockResolvedValueOnce(audio)
			.mockImplementation(() => new Promise((r) => (nextTurn = r)));
		vi.mocked(transcribe).mockResolvedValue('how many calls today');
		const onSend = vi.fn();
		const view = render(ConductorComposer, { onSend, lastReply: null });

		await fireEvent.click(screen.getByRole('button', { name: 'Talk hands-free' }));
		expect(unlockAudio).toHaveBeenCalled();
		await flush();
		await flush();
		expect(onSend).toHaveBeenCalledWith('how many calls today', 'how many calls today');
		expect(screen.getByText(/Waiting for the answer/)).toBeTruthy();

		await view.rerender({ onSend, lastReply: 'You have two calls today.' });
		await flush();
		expect(speak).toHaveBeenCalledWith('You have two calls today.');
		await flush();
		expect(vi.mocked(recordTurn).mock.calls.length).toBe(3); // listening again
		expect(screen.getByText(/Listening/)).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: 'End hands-free talk' }));
		nextTurn(null);
		expect(releaseMic).toHaveBeenCalled();
		expect(screen.queryByText(/Listening/)).toBeNull();
	});

	it('says what went wrong instead of going quiet', async () => {
		vi.mocked(recordTurn).mockReset().mockRejectedValueOnce(new Error('Permission denied'));
		const onError = vi.fn();
		render(ConductorComposer, { onSend: vi.fn(), lastReply: null, onError });
		await fireEvent.click(screen.getByRole('button', { name: 'Talk hands-free' }));
		await flush();
		expect(onError).toHaveBeenCalledWith('Voice: Permission denied');
		expect(screen.getByRole('button', { name: 'Talk hands-free' })).toBeTruthy();
	});
});
