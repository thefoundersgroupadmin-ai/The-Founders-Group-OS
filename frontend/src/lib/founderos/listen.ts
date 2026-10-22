/** Hearing the operator without the browser's speech recognition (which the
    desktop app and the iPhone home-screen app don't have): record one turn
    with the microphone, starting when speech starts and stopping after a
    pause, then POST /api/founderos/pages/voice/listen, which transcribes it on
    the OS Mac (whisper.cpp). The microphone stays open between turns. */
import { csrfHeaders, founderosUrl } from './api';

export type ListenOptions = {
	signal?: AbortSignal;
	/** silence after speech that ends the turn */
	pauseMs?: number;
	/** give up waiting for speech to start */
	waitMs?: number;
	/** longest turn */
	maxMs?: number;
	onSpeaking?: (speaking: boolean) => void;
};

let stream: MediaStream | null = null;
let ctx: AudioContext | null = null;

async function mic(): Promise<MediaStream> {
	if (stream && stream.getAudioTracks().some((t) => t.readyState === 'live')) return stream;
	stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } });
	return stream;
}

/** Close the microphone (the OS stops showing it as in use). */
export function releaseMic(): void {
	stream?.getTracks().forEach((t) => t.stop());
	stream = null;
	void ctx?.close().catch(() => {});
	ctx = null;
}

export function recorderType(): string {
	const R = typeof MediaRecorder === 'undefined' ? null : MediaRecorder;
	for (const t of ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg;codecs=opus']) {
		if (R?.isTypeSupported?.(t)) return t;
	}
	return '';
}

/** Is this RMS level speech? Calibrated against the room's own noise floor. */
export function isSpeech(level: number, floor: number): boolean {
	return level > Math.max(0.02, floor * 3);
}

/** Record one spoken turn; resolves with its audio, or null when nobody spoke. */
export async function recordTurn(o: ListenOptions = {}): Promise<Blob | null> {
	const { signal, pauseMs = 1300, waitMs = 20_000, maxMs = 60_000, onSpeaking } = o;
	const s = await mic();
	ctx ??= new AudioContext();
	if (ctx.state === 'suspended') await ctx.resume().catch(() => {});
	const src = ctx.createMediaStreamSource(s);
	const an = ctx.createAnalyser();
	an.fftSize = 1024;
	src.connect(an);
	const buf = new Float32Array(an.fftSize);
	const type = recorderType();
	const rec = new MediaRecorder(s, type ? { mimeType: type } : undefined);
	const chunks: Blob[] = [];
	rec.ondataavailable = (e) => e.data.size && chunks.push(e.data);
	rec.start(250);

	return new Promise((resolve) => {
		const t0 = performance.now();
		let floor = 0.01;
		let spoke = false;
		let lastVoice = 0;
		let done = false;
		const finish = (keep: boolean) => {
			if (done) return;
			done = true;
			clearInterval(timer);
			src.disconnect();
			onSpeaking?.(false);
			rec.onstop = () => resolve(keep && spoke ? new Blob(chunks, { type: rec.mimeType || type || 'audio/webm' }) : null);
			if (rec.state !== 'inactive') rec.stop();
			else resolve(null);
		};
		signal?.addEventListener('abort', () => finish(false), { once: true });
		const timer = setInterval(() => {
			an.getFloatTimeDomainData(buf);
			let sum = 0;
			for (const v of buf) sum += v * v;
			const level = Math.sqrt(sum / buf.length);
			const now = performance.now();
			if (isSpeech(level, floor)) {
				if (!spoke) onSpeaking?.(true);
				spoke = true;
				lastVoice = now;
			} else if (!spoke) {
				floor = floor * 0.95 + level * 0.05; // learn the room while waiting
			}
			if (spoke && now - lastVoice > pauseMs) finish(true);
			else if (!spoke && now - t0 > waitMs) finish(false);
			else if (now - t0 > maxMs) finish(true);
		}, 50);
	});
}

/** The words in a recording, transcribed on the OS Mac. */
export async function transcribe(audio: Blob, signal?: AbortSignal): Promise<string> {
	const res = await fetch(founderosUrl('/pages/voice/listen'), {
		method: 'POST',
		credentials: 'include',
		headers: { 'Content-Type': audio.type || 'application/octet-stream', ...(await csrfHeaders('POST')) },
		body: audio,
		signal
	});
	const body = (await res.json().catch(() => ({}))) as { text?: string; error?: string };
	if (!res.ok) throw new Error(body.error ?? `transcription failed (${res.status})`);
	return (body.text ?? '').trim();
}

/** One turn: record until a pause, then the words ('' when nobody spoke). */
export async function listenOnce(o: ListenOptions = {}): Promise<string> {
	const audio = await recordTurn(o);
	if (!audio || o.signal?.aborted) return '';
	return transcribe(audio, o.signal);
}

/** Browsers whose own speech recognition can't be trusted to work. */
export function needsServerListening(ua: string, hasRecognition: boolean): boolean {
	return !hasRecognition || /Electron\//.test(ua) || /iPhone|iPad|iPod/.test(ua);
}
