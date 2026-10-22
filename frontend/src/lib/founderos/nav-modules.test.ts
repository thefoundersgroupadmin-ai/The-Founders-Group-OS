import { describe, expect, it } from 'vitest';
import { NAV_ITEMS } from './nav';
import { founderosHrefsFor } from './commandModules';

// The /os sidebar only shows pages that are in the workspace module catalog
// (lib/config/workspaceModules.ts). A page added to nav.ts but not to the
// catalog is silently hidden for every real workspace, so keep them in step.
describe('nav and the workspace module catalog', () => {
	it('every sidebar page is switched on for a default workspace', () => {
		const enabled = founderosHrefsFor({});
		const missing = NAV_ITEMS.map((n) => n.href).filter((href) => !enabled.has(href));
		expect(missing).toEqual([]);
	});
});
