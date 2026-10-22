import { describe, expect, it } from 'vitest';
import { isSpeech, needsServerListening } from './listen';

describe('listening', () => {
	it('uses the Mac for the desktop app and iPhone, the browser elsewhere', () => {
		const electron = 'Mozilla/5.0 (Macintosh) AppleWebKit/537.36 BusinessOS/1.0.8 Chrome/120.0 Electron/28.3.3 Safari/537.36';
		expect(needsServerListening(electron, true)).toBe(true);
		expect(needsServerListening('Mozilla/5.0 (iPhone; CPU iPhone OS 18_7) Safari/604.1', true)).toBe(true);
		expect(needsServerListening('Mozilla/5.0 (Macintosh) Chrome/141.0 Safari/537.36', true)).toBe(false);
		expect(needsServerListening('Mozilla/5.0 Firefox/140.0', false)).toBe(true);
	});
	it('hears speech above the room, not the hum', () => {
		expect(isSpeech(0.01, 0.01)).toBe(false);
		expect(isSpeech(0.05, 0.01)).toBe(true);
		expect(isSpeech(0.05, 0.03)).toBe(false); // a noisy room needs a louder voice
	});
});
