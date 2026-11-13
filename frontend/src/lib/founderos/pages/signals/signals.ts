/** GET /api/founderos/pages/signals and its types. */
export type Signal = {
	kind: 'hiring' | 'funding' | 'tech' | 'engaged';
	title: string;
	detail?: string;
	url?: string;
	weight: number;
	date?: string;
};

export type Plays = {
	next: 'reply' | 'call' | 'email' | 'linkedin' | 'find_contact' | 'research';
	nextLabel: string;
	linkedinNote?: string;
	emailOpener?: string;
	callOpener?: string;
	offer?: string;
};

export type Row = {
	contactId: string;
	person: string;
	company: string;
	role: string;
	status: string;
	website: string;
	email: string;
	phone: string;
	linkedin: string;
	score: number;
	whyNow: string;
	signals: Signal[];
	checkedAt: string | null;
	problem?: string;
	plays: Plays;
};

export type SignalsBoard = {
	rows: Row[];
	stats: { Open: number; Checked: number; WithSignals: number; Hot: number; Hiring: number; Funded: number; NoEmail: number };
	lusha: { configured: boolean; usedToday: number; dailyCap: number };
	outreachPaused: boolean;
	autoChecks: boolean;
};

export type Filter = 'all' | 'hot' | 'hiring' | 'funded' | 'unchecked';

export function heat(score: number): 'hot' | 'warm' | 'cool' {
	return score >= 40 ? 'hot' : score >= 15 ? 'warm' : 'cool';
}

export function matches(r: Row, f: Filter): boolean {
	switch (f) {
		case 'hot':
			return r.score >= 40;
		case 'hiring':
			return r.signals.some((s) => s.kind === 'hiring' && s.weight >= 20);
		case 'funded':
			return r.signals.some((s) => s.kind === 'funding' && s.weight >= 25);
		case 'unchecked':
			return !r.checkedAt;
	}
	return true;
}

/** "3d ago" style age of the last check. */
export function ago(iso: string | null, now = new Date()): string {
	if (!iso) return 'not checked yet';
	const s = Math.max(0, (now.getTime() - new Date(iso).getTime()) / 1000);
	if (s < 3600) return `checked ${Math.max(1, Math.round(s / 60))}m ago`;
	if (s < 86400) return `checked ${Math.round(s / 3600)}h ago`;
	return `checked ${Math.round(s / 86400)}d ago`;
}
