// The brand file's hide list, with the real generated brand (the shared test
// setup clears it so the upstream nav suites keep pinning the full sidebar).
import { describe, expect, it, vi } from 'vitest';
import { NAV_OPERATE, visibleItems } from './nav';

describe('brand hide_views', () => {
	it('drops the hidden views and keeps everything else in order', async () => {
		const brand = await vi.importActual<typeof import('$lib/founderos/brand.gen')>('$lib/founderos/brand.gen');
		const visible = visibleItems(NAV_OPERATE, brand.HIDDEN_VIEWS).map((n) => n.href);
		for (const h of brand.HIDDEN_VIEWS) expect(visible).not.toContain(h);
		expect(visible[0]).toBe('/os');
		expect(visible).toEqual(NAV_OPERATE.map((n) => n.href).filter((h) => !brand.HIDDEN_VIEWS.includes(h)));
	});
	it('the TFG install hides trading, brand deals and AdPilot', async () => {
		const brand = await vi.importActual<typeof import('$lib/founderos/brand.gen')>('$lib/founderos/brand.gen');
		expect([...brand.HIDDEN_VIEWS].sort()).toEqual(['/os/adpilot', '/os/brand-deals', '/os/trading']);
	});
});
