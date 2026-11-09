// SvelteKit's built-in origin check refuses every cross-site form POST, which
// also blocked clients' website forms posting leads to TFG OS
// (/api/founderos/inbound/<token>, authenticated by its own per-client token;
// docs/tfg/CLIENT-PODS.md). The built-in check is switched off in
// svelte.config.js and enforced here with that single exemption.

const FORM_TYPES = ["application/x-www-form-urlencoded", "multipart/form-data", "text/plain"];
const UNSAFE = new Set(["POST", "PUT", "PATCH", "DELETE"]);
const EXEMPT = /^\/api\/founderos\/inbound\/[^/]+\/?$/;

export function isForbiddenCrossSiteForm(request: Request, url: URL): boolean {
  if (!UNSAFE.has(request.method)) return false;
  const type = (request.headers.get("content-type") ?? "").split(";")[0].trim().toLowerCase();
  if (!FORM_TYPES.includes(type)) return false;
  if (request.headers.get("origin") === url.origin) return false;
  return !EXEMPT.test(url.pathname);
}
