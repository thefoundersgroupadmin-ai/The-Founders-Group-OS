<!-- The Conductor composer (FounderOS v1 components/ConductorComposer.tsx):
     multiline (Enter sends, Shift+Enter breaks), grows to the text the browser
     actually laid out, (+) attaches a text file whose contents ride into the
     message honestly labeled, the live model chip, mic dictation through the
     browser's SpeechRecognition, a wave button that reads the last reply
     aloud (the OS's natural voice, else the browser's), and headphones for
     hands-free talk: speak, it sends when you pause, reads the answer aloud and
     listens again until you press it off. onSend(message, display): message is the full outbound body,
     display is what the caller shows as the user bubble. -->
<script lang="ts">
	import { AudioLines, Headphones, Mic, Plus, Send, X } from '$lib/founderos/icons';
	import { speak, stopSpeaking } from '$lib/founderos/voice';
	import '../kit/kit.css';
	import { COMPOSER_MAX_PX, composerHeight } from './conductor';

	let {
		onSend,
		disabled = false,
		model = null,
		placeholder = 'Message the Conductor…',
		lastReply = undefined,
		onError
	}: {
		onSend: (message: string, display: string) => void | Promise<void>;
		disabled?: boolean;
		model?: string | null;
		placeholder?: string;
		lastReply?: string | null;
		onError?: (message: string) => void;
	} = $props();

	let input = $state('');
	let attachment = $state<{ name: string; text: string } | null>(null);
	let listening = $state(false);
	let speaking = $state(false);
	let box: HTMLTextAreaElement | undefined = $state();
	let fileInput: HTMLInputElement | undefined = $state();
	let rec: { stop: () => void } | null = null;
	let dictationBase = '';
	// hands-free: listening → (pause) send → waiting for the reply → speaking → listening
	let talking = $state(false);
	let awaiting = $state(false);
	let replyBefore: string | null | undefined = null;
	let pauseTimer: ReturnType<typeof setTimeout> | null = null;
	const PAUSE_MS = 1400;

	// Reset to auto before measuring, or the box could only ever grow.
	$effect(() => {
		void input;
		void attachment;
		if (!box) return;
		box.style.height = 'auto';
		box.style.height = `${composerHeight(box.scrollHeight)}px`;
	});

	function send() {
		const text = input.trim();
		if (!text || disabled) return;
		const message = attachment ? `${text}\n\n[Attached: ${attachment.name}]\n${attachment.text}` : text;
		input = '';
		attachment = null;
		void onSend(message, text);
	}

	async function attach(file: File | undefined) {
		if (!file) return;
		try {
			attachment = { name: file.name, text: (await file.text()).slice(0, 8000) };
		} catch {
			onError?.(`Could not read ${file.name}`);
		}
	}

	type Recognition = {
		continuous: boolean;
		interimResults: boolean;
		lang: string;
		onresult: (e: { results: ArrayLike<ArrayLike<{ transcript: string }>> }) => void;
		onend: () => void;
		onerror: () => void;
		start: () => void;
		stop: () => void;
	};

	function toggleMic() {
		if (listening) {
			rec?.stop();
			return;
		}
		listen(false);
	}

	function listen(handsFree: boolean) {
		const w = window as unknown as { SpeechRecognition?: new () => Recognition; webkitSpeechRecognition?: new () => Recognition };
		const SR = w.SpeechRecognition ?? w.webkitSpeechRecognition;
		if (!SR) {
			onError?.('Voice input is not available in this browser.');
			return;
		}
		const r = new SR();
		r.continuous = true;
		r.interimResults = true;
		r.lang = 'en-US';
		dictationBase = input;
		r.onresult = (e) => {
			let heard = '';
			for (const res of Array.from(e.results)) heard += res[0].transcript;
			input = (dictationBase ? `${dictationBase} ` : '') + heard;
			if (handsFree) {
				if (pauseTimer) clearTimeout(pauseTimer);
				pauseTimer = setTimeout(() => {
					if (!talking || !input.trim()) return;
					rec?.stop();
					replyBefore = lastReply;
					awaiting = true;
					send();
				}, PAUSE_MS);
			}
		};
		r.onend = () => {
			listening = false;
			// browsers end recognition after a silence: keep the conversation open
			if (handsFree && talking && !awaiting && !speaking) setTimeout(() => talking && !awaiting && !speaking && !listening && listen(true), 250);
		};
		r.onerror = () => (listening = false);
		r.start();
		rec = r;
		listening = true;
	}

	async function say(text: string) {
		speaking = true;
		try {
			await speak(text);
		} finally {
			speaking = false;
		}
	}

	function toggleSpeak() {
		if (speaking) {
			stopSpeaking();
			speaking = false;
			return;
		}
		if (!lastReply) return;
		void say(lastReply);
	}

	function toggleTalk() {
		if (talking) {
			talking = false;
			awaiting = false;
			if (pauseTimer) clearTimeout(pauseTimer);
			rec?.stop();
			stopSpeaking();
			return;
		}
		talking = true;
		listen(true);
		if (!listening) talking = false; // no voice input in this browser (listen said so)
	}

	// The answer arrived: say it, then listen for the next thing.
	$effect(() => {
		const reply = lastReply;
		if (!talking || !awaiting || disabled || !reply || reply === replyBefore) return;
		awaiting = false;
		void say(reply).then(() => {
			if (talking) listen(true);
		});
	});
</script>

<div class="bn-pressable is-row bn-composer rounded-[10px] border px-3 pb-2 pt-2.5">
	<textarea
		bind:this={box}
		bind:value={input}
		onkeydown={(e) => {
			if (e.key === 'Enter' && !e.shiftKey) {
				e.preventDefault();
				send();
			}
		}}
		rows="1"
		style="max-height: {COMPOSER_MAX_PX}px"
		aria-label="Message the Conductor"
		{placeholder}
		class="bn-text w-full resize-none overflow-y-auto bg-transparent text-xs leading-relaxed focus:outline-none"
	></textarea>
	{#if attachment}
		<div class="mb-1.5 flex items-center gap-1.5">
			<span class="bn-chip bn-attach bn-muted flex items-center gap-1.5 rounded-full px-2 py-0.5 font-mono text-[9.5px]">
				{attachment.name}
				<button type="button" onclick={() => (attachment = null)} aria-label="Remove attachment" class="bn-pressable bn-ctl bn-dim"><X size={10} /></button>
			</span>
		</div>
	{/if}
	<div class="flex items-center gap-1">
		<input bind:this={fileInput} type="file" accept=".txt,.md,.csv,.json,.log" class="hidden" onchange={(e) => void attach(e.currentTarget.files?.[0])} />
		<button type="button" onclick={() => fileInput?.click()} title="Attach a text file — its contents ride into the message" aria-label="Attach a text file" class="bn-pressable is-dark bn-ctl bn-dim rounded-full p-1.5">
			<Plus size={14} />
		</button>
		<span class="bn-dim hidden font-mono text-[9px] sm:inline">Enter sends · Shift+Enter breaks</span>
		{#if model}
			<span class="bn-dim ml-auto mr-1 font-mono text-[10px]" title="the model holding the Conductor seat right now">{model}</span>
		{/if}
		<button
			type="button"
			onclick={toggleMic}
			title={listening ? 'Stop dictation' : 'Dictate with your voice'}
			aria-label={listening ? 'Stop dictation' : 'Dictate with your voice'}
			class="bn-pressable bn-ctl rounded-full p-1.5 {listening ? 'bn-err' : 'is-dark bn-dim'} {model ? '' : 'ml-auto'}"
		>
			<Mic size={14} />
		</button>
		{#if lastReply !== undefined}
			<button
				type="button"
				onclick={toggleTalk}
				title={talking ? 'End hands-free talk' : 'Talk hands-free: speak, pause, hear the answer'}
				aria-label={talking ? 'End hands-free talk' : 'Talk hands-free'}
				aria-pressed={talking}
				class="bn-pressable bn-ctl rounded-full p-1.5 {talking ? 'bn-accent' : 'is-dark bn-dim'}"
			>
				<Headphones size={14} />
			</button>
		{/if}
		<button
			type="button"
			onclick={toggleSpeak}
			title={speaking ? 'Stop reading' : 'Read the last reply aloud'}
			aria-label={speaking ? 'Stop reading' : 'Read the last reply aloud'}
			class="bn-pressable bn-ctl rounded-full p-1.5 {speaking ? 'bn-accent' : 'is-dark bn-dim'}"
		>
			<AudioLines size={14} />
		</button>
		<button type="button" onclick={send} disabled={disabled || !input.trim()} aria-label="Send" class="bn-pressable is-dark bn-send bn-text rounded-full border p-1.5 disabled:opacity-40">
			<Send size={12} />
		</button>
	</div>
</div>

<style>
	/* ConductorComposer.tsx: a row lens that brightens its edge on focus */
	.bn-composer {
		border-color: var(--bn-border);
		background: var(--bn-bg);
	}
	.bn-composer:focus-within {
		border-color: var(--bn-border-strong);
	}
	textarea {
		/* BusinessOS styles bare textareas; the composer box is the surface here */
		background: transparent;
		border: 0;
		box-shadow: none;
		padding: 0;
	}
	textarea::placeholder {
		color: var(--bn-text-3);
	}
	.bn-ctl:hover {
		color: var(--bn-text);
	}
	.bn-attach {
		background: var(--bn-surface-2);
	}
	.bn-err {
		color: var(--bn-err);
	}
	.bn-send {
		border-color: var(--bn-border-strong);
		background: var(--bn-surface-2);
	}
</style>
