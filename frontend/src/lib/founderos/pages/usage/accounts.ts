/** GET /api/founderos/pages/ai-accounts: which AI logins this person's OS
    calls run on, and whether their laptop reports usage. No secret ever
    comes back; a saved login only reads as connected. */
export type CodexLogin = {
	state: 'waiting' | 'done' | 'failed' | 'none';
	link?: string;
	code?: string;
	error?: string;
};

export type AiAccounts = {
	owner: boolean;
	claude: { connected: boolean; via: string };
	codex: { connected: boolean; via: string; login: CodexLogin };
	laptop: { connected: boolean; issued: boolean; device: string; lastPushAt: string | null };
};

export type LaptopLine = { command: string; device: string };

/** The one-line status a provider row shows. */
export function loginLine(a: { connected: boolean; via: string }): string {
	return a.connected ? `connected · runs on ${a.via}` : 'not connected · AI buttons stay off for you until you add it';
}

export function laptopLine(l: AiAccounts['laptop'], now = Date.now()): string {
	if (l.connected && l.lastPushAt) {
		const mins = Math.max(0, Math.round((now - Date.parse(l.lastPushAt)) / 60_000));
		return `reporting · last reading ${mins < 1 ? 'just now' : mins < 60 ? `${mins}m ago` : `${Math.round(mins / 60)}h ago`}`;
	}
	return l.issued ? 'install line made · waiting for the first reading' : 'not connected';
}
