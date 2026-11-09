import { describe, expect, it } from "vitest";
import { isForbiddenCrossSiteForm } from "./csrf";

const url = (p: string) => new URL("https://os.thefoundersgroup.net" + p);
const post = (origin: string | null, type = "application/x-www-form-urlencoded") =>
  new Request("https://os.thefoundersgroup.net/x", {
    method: "POST",
    headers: { "content-type": type, ...(origin ? { origin } : {}) },
    body: "a=1",
  });

describe("cross-site form check", () => {
  it("lets a client's website form post a lead", () => {
    expect(isForbiddenCrossSiteForm(post("https://client.example"), url("/api/founderos/inbound/tok123"))).toBe(false);
  });
  it("blocks cross-site form posts everywhere else", () => {
    expect(isForbiddenCrossSiteForm(post("https://evil.example"), url("/os/settings"))).toBe(true);
    expect(isForbiddenCrossSiteForm(post(null), url("/api/founderos/pages/pods"))).toBe(true);
    expect(isForbiddenCrossSiteForm(post("https://evil.example"), url("/api/founderos/inbound/tok/../../pods"))).toBe(true);
  });
  it("allows same-origin forms and non-form requests", () => {
    expect(isForbiddenCrossSiteForm(post("https://os.thefoundersgroup.net"), url("/os/settings"))).toBe(false);
    expect(isForbiddenCrossSiteForm(post("https://evil.example", "application/json"), url("/os/settings"))).toBe(false);
  });
});
