/** The OS's voice: POST /api/founderos/pages/voice/speak returns a natural
    voice (ElevenLabs) as audio/mpeg; when it can't (no key, the month's
    characters spent) it answers 503 { fallback: true } and the browser's own
    voice reads the text instead. One thing speaks at a time. */
import { csrfHeaders, founderosUrl } from './api';

let current: { stop: () => void } | null = null;

/** Stop whatever is speaking. */
export function stopSpeaking(): void {
	current?.stop();
	current = null;
}

export function isSpeaking(): boolean {
	return current !== null;
}

/** Markdown and links read badly aloud. */
export function plainForSpeech(text: string): string {
	return text
		.replace(/```[\s\S]*?```/g, ' (code on screen) ')
		.replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
		.replace(/https?:\/\/\S+/g, 'a link')
		.replace(/[*_`#>]+/g, '')
		.replace(/\s+/g, ' ')
		.trim();
}

function browserVoice(text: string): Promise<void> {
	return new Promise((resolve) => {
		if (typeof speechSynthesis === 'undefined') return resolve();
		const u = new SpeechSynthesisUtterance(text);
		const done = () => {
			if (current === handle) current = null;
			resolve();
		};
		const handle = {
			stop: () => {
				speechSynthesis.cancel();
				done();
			}
		};
		u.onend = done;
		u.onerror = done;
		current = handle;
		speechSynthesis.speak(u);
	});
}

/** Speak text and resolve when it has finished (or was stopped). */
export async function speak(text: string): Promise<void> {
	stopSpeaking();
	const said = plainForSpeech(text);
	if (!said) return;
	let audio: HTMLAudioElement | null = null;
	try {
		const res = await fetch(founderosUrl('/pages/voice/speak'), {
			method: 'POST',
			credentials: 'include',
			headers: { 'Content-Type': 'application/json', ...(await csrfHeaders('POST')) },
			body: JSON.stringify({ text: said })
		});
		if (res.ok && (res.headers.get('content-type') ?? '').startsWith('audio/')) {
			audio = new Audio(URL.createObjectURL(await res.blob()));
		}
	} catch {
		audio = null;
	}
	if (!audio) return browserVoice(said);
	const a = audio;
	return new Promise((resolve) => {
		const done = () => {
			URL.revokeObjectURL(a.src);
			if (current === handle) current = null;
			resolve();
		};
		const handle = {
			stop: () => {
				a.pause();
				done();
			}
		};
		a.onended = done;
		a.onerror = done;
		current = handle;
		a.play().catch(() => {
			// autoplay refused (no click on the page yet): the browser voice may still work
			done();
			void browserVoice(said);
		});
	});
}
