/** GET /api/founderos/pages/dialer and the call types. */
export type Prospect = {
	contactId: string;
	name: string;
	person: string;
	company: string;
	role: string;
	email: string;
	phone: string;
	localTime: string;
	zone: string;
	status: string;
	likelihood: number;
	attempts: number;
	callbackDue: boolean;
	lastOutcome?: string;
	lastCallAt?: string;
	brief?: string;
};

export type Call = { id: string; contactId: string; to: string; status: string; startedAt: string; durationS?: number; outcome?: string };

export type DialerBoard = {
	twilio: { configured: boolean; ready: boolean; callerId: string };
	apollo: { configured: boolean };
	queue: Prospect[];
	stats: { dials: number; connects: number; meetings: number; talkSeconds: number };
	contacts: { withPhone: number; noPhone: number };
};

/** The outcome buttons, in the order a caller reaches for them. */
export const OUTCOMES: { id: string; label: string; key: string }[] = [
	{ id: 'meeting', label: 'Meeting booked', key: '1' },
	{ id: 'interested', label: 'Interested', key: '2' },
	{ id: 'callback', label: 'Call back', key: '3' },
	{ id: 'no_answer', label: 'No answer', key: '4' },
	{ id: 'voicemail', label: 'Voicemail', key: '5' },
	{ id: 'gatekeeper', label: 'Gatekeeper', key: '6' },
	{ id: 'not_interested', label: 'Not interested', key: '7' },
	{ id: 'wrong_number', label: 'Wrong number', key: '8' },
	{ id: 'do_not_call', label: 'Do not call', key: '9' }
];

/** Outcomes worth a follow-up email. */
export const FOLLOW_UP = new Set(['meeting', 'interested', 'callback']);

export function clock(seconds: number): string {
	const s = Math.max(0, Math.floor(seconds));
	return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

/** +12127365000 → (212) 736-5000; anything else as given. */
export function prettyPhone(e164: string): string {
	const m = /^\+1(\d{3})(\d{3})(\d{4})$/.exec(e164);
	return m ? `(${m[1]}) ${m[2]}-${m[3]}` : e164;
}

/** A callback default: the next weekday at 10:00, local to the browser. */
export function nextMorning(now = new Date()): string {
	const d = new Date(now);
	d.setDate(d.getDate() + 1);
	while (d.getDay() === 0 || d.getDay() === 6) d.setDate(d.getDate() + 1);
	d.setHours(10, 0, 0, 0);
	const pad = (n: number) => String(n).padStart(2, '0');
	return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
