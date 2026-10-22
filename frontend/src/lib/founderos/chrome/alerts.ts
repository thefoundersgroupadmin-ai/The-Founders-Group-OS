/** Alerts: how the OS reaches you. GET /api/founderos/pages/notify is this
    person's phones and choices; the feed (/pages/notify/feed) is what the
    open OS reads aloud. */
import { writable } from 'svelte/store';

export type AlertKind = { kind: string; label: string; on: boolean };
export type AlertDevice = { endpoint: string; label: string; addedAt: string };
export type AlertStatus = {
	publicKey: string;
	devices: AlertDevice[];
	kinds: AlertKind[];
	briefHour: number;
	voice: { natural: boolean; detail: string };
};
export type AlertEvent = { id: string; kind: string; title: string; body: string; url: string; at: string };

/** The bell drawer. */
export const alertsOpen = writable(false);

/** VAPID public key (base64url) → the bytes PushManager.subscribe wants. */
export function keyBytes(base64url: string): Uint8Array {
	const pad = '='.repeat((4 - (base64url.length % 4)) % 4);
	const raw = atob((base64url + pad).replace(/-/g, '+').replace(/_/g, '/'));
	return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

/** A name for this device the list can show. */
export function deviceLabel(ua: string): string {
	if (/iPhone/.test(ua)) return 'iPhone';
	if (/iPad/.test(ua)) return 'iPad';
	if (/Android/.test(ua)) return 'Android phone';
	const browser = /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox' : /Safari\//.test(ua) ? 'Safari' : 'Browser';
	const os = /Mac OS X/.test(ua) ? 'Mac' : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : '';
	return os ? `${browser} on ${os}` : browser;
}

/** iPhone and iPad only allow push from the home-screen app. */
export function needsHomeScreen(ua: string, standalone: boolean): boolean {
	return /iPhone|iPad|iPod/.test(ua) && !standalone;
}

export function hourLabel(h: number): string {
	const ampm = h < 12 ? 'am' : 'pm';
	const twelve = h % 12 === 0 ? 12 : h % 12;
	return `${twelve}${ampm}`;
}

const SPEAK_KEY = 'founderos-speak-alerts';

/** "Read alerts aloud while the OS is open": per browser. */
export function readSpeakAlerts(): boolean {
	try {
		return localStorage.getItem(SPEAK_KEY) === '1';
	} catch {
		return false;
	}
}

export function writeSpeakAlerts(on: boolean): void {
	try {
		localStorage.setItem(SPEAK_KEY, on ? '1' : '0');
	} catch {
		/* private window: it simply won't be remembered */
	}
}

/** What the open OS says for one event. */
export function spokenLine(e: Pick<AlertEvent, 'title' | 'body'>): string {
	return e.body ? `${e.title}. ${e.body}` : e.title;
}

export type VoiceOption = { id: string; name: string; description: string; gender: string; accent: string; age: string; preview: string };
export type VoiceList = { voices: VoiceOption[]; current: string; error?: string };

/** Voices matching the filters ('' is any), one per name, in the account's order. */
export function filterVoices(voices: VoiceOption[], accent: string, gender: string): VoiceOption[] {
	const seen = new Set<string>();
	return voices.filter((v) => {
		if ((accent && v.accent !== accent) || (gender && v.gender !== gender) || seen.has(v.name)) return false;
		seen.add(v.name);
		return true;
	});
}
