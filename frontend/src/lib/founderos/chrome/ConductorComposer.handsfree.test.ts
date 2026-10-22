// Hands-free talk: speak, pause, it sends; the answer is read aloud; it listens again.
import { fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('$lib/founderos/voice', () => ({ speak: vi.fn(() => Promise.resolve()), stopSpeaking: vi.fn() }));

import { speak } from '$lib/founderos/voice';
import ConductorComposer from './ConductorComposer.svelte';

type Rec = { onresult: (e: unknown) => void; onend: () => void; start: () => void; stop: () => void };
const started: Rec[] = [];
class FakeRecognition {
	continuous = false;
	interimResults = false;
	lang = '';
	onresult: (e: unknown) => void = () => {};
	onend: () => void = () => {};
	onerror: () => void = () => {};
	start() {
		started.push(this);
	}
	stop() {
		this.onend();
	}
}

afterEach(() => {
	vi.useRealTimers();
	started.length = 0;
	delete (window as unknown as Record<string, unknown>).SpeechRecognition;
});

describe('hands-free talk', () => {
	it('is offered only where there is a reply to read', () => {
		render(ConductorComposer, { onSend: vi.fn() });
		expect(screen.queryByRole('button', { name: 'Talk hands-free' })).toBeNull();
	});

	it('sends after a pause, reads the answer, then listens again', async () => {
		vi.useFakeTimers();
		(window as unknown as Record<string, unknown>).SpeechRecognition = FakeRecognition;
		const onSend = vi.fn();
		const view = render(ConductorComposer, { onSend, lastReply: null });
		await fireEvent.click(screen.getByRole('button', { name: 'Talk hands-free' }));
		expect(started).toHaveLength(1);
		started[0].onresult({ results: [[{ transcript: 'how many calls today' }]] });
		await vi.advanceTimersByTimeAsync(1500);
		expect(onSend).toHaveBeenCalledWith('how many calls today', 'how many calls today');

		await view.rerender({ onSend, lastReply: 'You have two calls today.' });
		await vi.advanceTimersByTimeAsync(10);
		expect(speak).toHaveBeenCalledWith('You have two calls today.');
		expect(started.length).toBeGreaterThanOrEqual(2);

		await fireEvent.click(screen.getByRole('button', { name: 'End hands-free talk' }));
		expect(screen.getByRole('button', { name: 'Talk hands-free' })).toBeTruthy();
	});
});
