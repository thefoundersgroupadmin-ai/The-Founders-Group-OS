import type { Handle } from "@sveltejs/kit";
import { env } from "$env/dynamic/private";

function setSecurityHeaders(response: Response): Response {
  response.headers.set("X-Frame-Options", "SAMEORIGIN");
  response.headers.set("X-Content-Type-Options", "nosniff");
  response.headers.set("Referrer-Policy", "strict-origin-when-cross-origin");
  response.headers.set(
    "Permissions-Policy",
    "camera=(), microphone=(self), geolocation=()",
  );
  response.headers.set("X-XSS-Protection", "1; mode=block");
  return response;
}

function isInternalApiPath(pathname: string): boolean {
  let decoded: string;
  try {
    decoded = decodeURIComponent(pathname);
  } catch {
    return true;
  }
  return /^\/+api\/+(v\d+\/+)?internal(\/|$)/i.test(decoded);
}

export const handle: Handle = async ({ event, resolve }) => {
  // Same-origin /api/* proxy for the adapter-node (Docker) deployment.
  // The web frontend always calls its own origin (see lib/config/runtime.ts);
  // in production Cloudflare Pages proxies /api/* to the backend and in dev
  // the Vite server does. This is the equivalent for the containerized node
  // server. Unset BACKEND_INTERNAL_URL disables it (e.g. behind an external
  // proxy).
  const backend = env.BACKEND_INTERNAL_URL;
  if (backend && event.url.pathname.startsWith("/api/")) {
    // Service-to-service routes (/api[/vN]/internal/*) trust the caller's
    // address, and every request through this proxy comes from this machine.
    // The backend decodes the path, so check the decoded form.
    if (isInternalApiPath(event.url.pathname)) {
      return setSecurityHeaders(new Response("Not found", { status: 404 }));
    }
    const target = new URL(
      event.url.pathname + event.url.search,
      backend,
    ).toString();
    const headers = new Headers(event.request.headers);
    headers.delete("host");
    headers.delete("connection");
    // Hop-by-hop framing: fetch() frames the body itself and rejects a copied
    // Transfer-Encoding (Cloudflare sends body-less POSTs chunked), which made
    // every body-less POST through the tunnel a 500.
    headers.delete("transfer-encoding");
    headers.delete("content-length");
    headers.delete("keep-alive");
    const body =
      event.request.method === "GET" || event.request.method === "HEAD"
        ? undefined
        : await event.request.arrayBuffer();
    const upstream = await fetch(target, {
      method: event.request.method,
      headers,
      body: body && body.byteLength > 0 ? body : undefined,
      redirect: "manual",
    });
    // Copy: headers on a fetch() Response are immutable.
    return setSecurityHeaders(new Response(upstream.body, upstream));
  }

  return setSecurityHeaders(await resolve(event));
};
