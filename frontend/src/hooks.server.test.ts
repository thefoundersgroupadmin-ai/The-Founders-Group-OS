import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('$env/dynamic/private', () => ({ env: { BACKEND_INTERNAL_URL: 'http://backend.test' } }));

const { handle } = await import('./hooks.server');

function call(path: string) {
	const url = new URL('https://os.example.test' + path);
	const event = { url, request: new Request(url) } as Parameters<typeof handle>[0]['event'];
	return handle({ event, resolve: async () => new Response('page') });
}

describe('the /api proxy', () => {
	afterEach(() => vi.unstubAllGlobals());

	it('forwards /api to the backend', async () => {
		const fetchMock = vi.fn(async (_url: string | URL | Request, _init?: RequestInit) => new Response('ok'));
		vi.stubGlobal('fetch', fetchMock);
		const res = await call('/api/founderos/pages/home');
		expect(await res.text()).toBe('ok');
		expect(String(fetchMock.mock.calls[0][0])).toBe('http://backend.test/api/founderos/pages/home');
	});

	// Cloudflare forwards a body-less POST (e.g. Usage → Resume) chunked; fetch()
	// rejects a copied Transfer-Encoding, which turned it into a 500.
	it('drops hop-by-hop framing and sends a body-less POST without a body', async () => {
		const fetchMock = vi.fn(async (_url: string | URL | Request, _init?: RequestInit) => new Response('ok'));
		vi.stubGlobal('fetch', fetchMock);
		const url = new URL('https://os.example.test/api/founderos/pages/usage/ai/resume');
		const request = new Request(url, { method: 'POST', headers: { 'transfer-encoding': 'chunked', 'content-length': '0', 'x-csrf-token': 't' } });
		const res = await handle({ event: { url, request } as Parameters<typeof handle>[0]['event'], resolve: async () => new Response('page') });
		expect(await res.text()).toBe('ok');
		const init = fetchMock.mock.calls[0][1]!;
		const sent = new Headers(init.headers);
		expect(sent.has('transfer-encoding')).toBe(false);
		expect(sent.has('content-length')).toBe(false);
		expect(sent.get('x-csrf-token')).toBe('t');
		expect(init.body).toBeUndefined();
	});

	// Service-to-service routes trust the caller's address, and every proxied
	// request comes from this machine, so the public side must never reach them.
	it.each(['/api/internal/osa/workspaces', '/api/v1/internal/osa/health', '/api/INTERNAL/osa/health', '/api/%69nternal/osa/health'])(
		'refuses %s',
		async (path) => {
			const fetchMock = vi.fn(async (_url: string | URL | Request, _init?: RequestInit) => new Response('ok'));
			vi.stubGlobal('fetch', fetchMock);
			const res = await call(path);
			expect(res.status).toBe(404);
			expect(fetchMock).not.toHaveBeenCalled();
		},
	);
});
