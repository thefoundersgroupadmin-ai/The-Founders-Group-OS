/** The browser phone: one Twilio Device per tab, outgoing only. The token
    comes from GET /api/founderos/pages/dialer/token and is renewed before it
    runs out. A call passes the OS's call id; the voice hook dials the number
    from that row, so the browser never chooses who gets rung. */
import { founderosFetch } from '$lib/founderos/api';

export type CallState = 'connecting' | 'ringing' | 'open' | 'ended';

export type LiveCall = {
	state: CallState;
	muted: boolean;
	hangUp: () => void;
	mute: (on: boolean) => void;
	digits: (d: string) => void;
};

type DeviceLike = {
	register: () => Promise<void>;
	connect: (o: { params: Record<string, string> }) => Promise<CallLike>;
	updateToken: (t: string) => void;
	on: (e: string, f: (...a: unknown[]) => void) => void;
	destroy: () => void;
};
type CallLike = {
	on: (e: string, f: (...a: unknown[]) => void) => void;
	disconnect: () => void;
	mute: (on: boolean) => void;
	sendDigits: (d: string) => void;
};

let device: DeviceLike | null = null;

async function token(): Promise<string> {
	const r = await founderosFetch<{ token: string }>('/pages/dialer/token');
	return r.token;
}

/** The Twilio library, loaded the first time the dialer needs it. */
export let loadDevice = async (t: string): Promise<DeviceLike> => {
	const { Device } = await import('@twilio/voice-sdk');
	return new Device(t, { closeProtection: true, codecPreferences: ['opus', 'pcmu'] as never }) as unknown as DeviceLike;
};

export async function ready(): Promise<DeviceLike> {
	if (device) return device;
	const d = await loadDevice(await token());
	d.on('tokenWillExpire', () => {
		void token().then((t) => d.updateToken(t)).catch(() => {});
	});
	device = d;
	return d;
}

export function shutdown(): void {
	device?.destroy();
	device = null;
}

/** Place the OS's call `callId`; onChange hears each state. */
export async function dial(callId: string, onChange: (c: LiveCall) => void, onError: (m: string) => void): Promise<LiveCall> {
	const d = await ready();
	const call = await d.connect({ params: { CallId: callId } });
	const live: LiveCall = {
		state: 'connecting',
		muted: false,
		hangUp: () => call.disconnect(),
		mute: (on) => {
			call.mute(on);
			live.muted = on;
			onChange({ ...live });
		},
		digits: (x) => call.sendDigits(x)
	};
	const set = (s: CallState) => {
		live.state = s;
		onChange({ ...live });
	};
	call.on('ringing', () => set('ringing'));
	call.on('accept', () => set('open'));
	call.on('disconnect', () => set('ended'));
	call.on('cancel', () => set('ended'));
	call.on('reject', () => set('ended'));
	call.on('error', (e) => {
		onError((e as { message?: string })?.message ?? 'call failed');
		set('ended');
	});
	onChange({ ...live });
	return live;
}
