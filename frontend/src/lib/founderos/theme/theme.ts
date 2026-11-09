// One interface for everyone (owner's call, 9 Nov 2026): the operator UI always wears
// Portal, the client portal's frosted glass over fading colours. The older skins'
// token blocks stay in monolith.css, but they can no longer be picked; any stored
// choice (including production's 'dark') resolves to Portal.
export const FOUNDEROS_THEMES = ['portal'] as const;
export type FounderosTheme = (typeof FOUNDEROS_THEMES)[number];
export const DEFAULT_FOUNDEROS_THEME: FounderosTheme = 'portal';
export const STORAGE_KEY = 'founderos-theme';

/** The skins painted on a light canvas. BusinessOS's own `.dark` styles must
 *  be off under them, or its components paint dark panels on a white page. */
const LIGHT_THEMES: readonly FounderosTheme[] = ['portal'];

/** Picker metadata (FounderOS v1 THEME_META): name, one-line feel, [bg, accent, text] swatch. */
export const THEME_META: Record<FounderosTheme, { name: string; blurb: string; swatch: [string, string, string] }> = {
	portal: { name: 'Portal', blurb: "frosted glass over the client portal's fading colours", swatch: ['#efe7da', '#a8874f', '#1a1a1c'] }
};

/** Production's id for a skin the bridge names differently. */
const ALIASES: Record<string, FounderosTheme> = Object.fromEntries(
	['dark', 'mono', 'mono-light', 'terminal', 'light', 'midnight', 'ember', 'glass', 'glass-light'].map((k) => [k, 'portal' as FounderosTheme])
);

export function isFounderosTheme(v: unknown): v is FounderosTheme {
	return typeof v === 'string' && (FOUNDEROS_THEMES as readonly string[]).includes(v);
}

export function isDarkTheme(theme: FounderosTheme): boolean {
	return !LIGHT_THEMES.includes(theme);
}

/** A stored or on-page value as a skin, or the default. */
export function resolveTheme(v: string | null | undefined): FounderosTheme {
	const id = v != null && v in ALIASES ? ALIASES[v] : v;
	return isFounderosTheme(id) ? id : DEFAULT_FOUNDEROS_THEME;
}

export function readStoredTheme(): FounderosTheme {
	try {
		return resolveTheme(localStorage.getItem(STORAGE_KEY));
	} catch {
		return DEFAULT_FOUNDEROS_THEME;
	}
}

export function applyFounderosTheme(theme: FounderosTheme): void {
	const root = document.documentElement;
	root.setAttribute('data-founderos-theme', theme);
	root.classList.toggle('dark', isDarkTheme(theme));
	try {
		localStorage.setItem(STORAGE_KEY, theme);
	} catch {
		// Private mode: the theme still applies for this session.
	}
}

/** Leaving /os for a BusinessOS page: drop the operator's skin and hand the root
 *  back to BusinessOS's own light/dark choice. */
export function leaveFounderosTheme(businessOsDark: boolean): void {
	const root = document.documentElement;
	root.removeAttribute('data-founderos-theme');
	root.classList.toggle('dark', businessOsDark);
}

// Inline in app.html so the first paint of an /os page is already skinned (no
// flash). Only /os is skinned, and BusinessOS's own 'theme' is never written:
// pinning it to dark here once flipped the whole desktop dark.
export const FOUNDEROS_THEME_INIT_SCRIPT = `(function(){var p=location.pathname;if(p!=='/os'&&p.indexOf('/os/')!==0)return;var k=${JSON.stringify([...FOUNDEROS_THEMES])},l=${JSON.stringify([...LIGHT_THEMES])},t;try{t=localStorage.getItem('${STORAGE_KEY}')}catch(e){}if(k.indexOf(t)<0)t='${DEFAULT_FOUNDEROS_THEME}';var r=document.documentElement;r.setAttribute('data-founderos-theme',t);if(l.indexOf(t)<0)r.classList.add('dark');else r.classList.remove('dark');})();`;
