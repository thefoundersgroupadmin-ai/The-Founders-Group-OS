import { describe, expect, it } from 'vitest';
import { deviceLabel, hourLabel, keyBytes, needsHomeScreen, spokenLine } from './alerts';
import { plainForSpeech } from '../voice';

const IPHONE = 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1';
const MAC_CHROME = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36';

describe('alerts helpers', () => {
	it('decodes a base64url VAPID key', () => {
		expect([...keyBytes('AQID_-8')]).toEqual([1, 2, 3, 255, 239]);
	});
	it('names the device and knows an iPhone needs the home-screen app', () => {
		expect(deviceLabel(IPHONE)).toBe('iPhone');
		expect(deviceLabel(MAC_CHROME)).toBe('Chrome on Mac');
		expect(needsHomeScreen(IPHONE, false)).toBe(true);
		expect(needsHomeScreen(IPHONE, true)).toBe(false);
		expect(needsHomeScreen(MAC_CHROME, false)).toBe(false);
	});
	it('reads hours and alerts the way a person says them', () => {
		expect([hourLabel(0), hourLabel(8), hourLabel(12), hourLabel(17)]).toEqual(['12am', '8am', '12pm', '5pm']);
		expect(spokenLine({ title: 'Reply from Dana', body: 'Re: call' })).toBe('Reply from Dana. Re: call');
	});
	it('strips markdown and links before speaking', () => {
		expect(plainForSpeech('**Two** calls. See [the funnel](https://x.y/z) or https://a.b/c\n\n```js\nx()\n```')).toBe(
			'Two calls. See the funnel or a link (code on screen)'
		);
	});
});
