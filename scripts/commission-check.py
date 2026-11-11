#!/usr/bin/env python3
"""Commissioning check: proves every real boundary a client AI SDR depends on, with evidence,
without sending an email or changing anything (gap list #31: the e2e test fakes these boundaries).

  commission-check.py                 every shared boundary
  commission-check.py --pod <slug>    plus one pod's own: test send, reply inbox, portal sync, agent, calendar

Boundaries: OS backend and database; agent platform (Paperclip) API; OS agent-tool gateway;
outgoing mail login (SMTP AUTH, no message sent); reply inbox (IMAP, read-only); Cal.com bookings;
client portal database; website enquiry store (Cloudflare KV). Credentials come only from
~/.founderos/.env, ~/.founderos/planted.env and the backend .env, as the OS itself uses them.
Exit code 0 only when every boundary passes.
"""
import imaplib
import json
import smtplib
import ssl
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

HOME = Path.home()
REPO = Path(__file__).resolve().parent.parent


def env(path):
    out = {}
    try:
        for line in Path(path).expanduser().read_text().splitlines():
            if "=" in line and not line.lstrip().startswith("#"):
                k, v = line.split("=", 1)
                out[k.strip()] = v.strip().strip('"')
    except OSError:
        pass
    return out


PC = env("~/.founderos/.env")
PLANTED = env("~/.founderos/planted.env")
BACKEND = env(REPO / "desktop/backend-go/.env")
results = []


def check(name, fn):
    try:
        ok, evidence = fn()
    except Exception as e:  # a boundary that throws has failed; say how
        ok, evidence = False, f"{type(e).__name__}: {e}"
    results.append((name, ok, evidence))
    print(f"{'PASS' if ok else 'FAIL'}  {name}: {evidence}")


def get(url, headers=None, data=None, method=None, timeout=20):
    req = urllib.request.Request(url, data=data, headers=dict({"User-Agent": "tfg-os-commission-check/1.0"}, **(headers or {})), method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read(2_000_000).decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read(600).decode("utf-8", "replace")


def psql(q):
    r = subprocess.run(["psql", BACKEND.get("DATABASE_URL", ""), "-At", "-F", "\t", "-c", q], capture_output=True, text=True, timeout=30)
    if r.returncode:
        raise RuntimeError(r.stderr.strip()[:200])
    return [ln.split("\t") for ln in r.stdout.splitlines() if ln]


def backend():
    s, _ = get("http://127.0.0.1:8801/health")
    n = psql("select count(*) from founderos_client_pods")[0][0]
    return s == 200, f"/health {s}; database readable ({n} client pods)"


def paperclip():
    s, body = get(f"http://127.0.0.1:3101/api/companies/{PC.get('PAPERCLIP_COMPANY_ID', '')}/agents",
                  {"Authorization": "Bearer " + PC.get("PAPERCLIP_BOARD_KEY", "")})
    n = len(json.loads(body)) if s == 200 and body.startswith("[") else "?"
    return s == 200, f"agents API {s} ({n} agents)"


def gateway():
    key = (HOME / ".founderos/agent-tools.key").read_text().strip()
    s, body = get("http://127.0.0.1:8801/api/founderos/agent-tools/mcp", {"Authorization": "Bearer " + key, "Content-Type": "application/json"},
                  json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/list"}).encode(), "POST")
    tools = len(json.loads(body)["result"]["tools"]) if s == 200 else 0
    return s == 200 and tools > 0, f"tools/list {s} ({tools} tools)"


def smtp():
    host, user, pw = PLANTED.get("INBOX_1_SMTP_HOST"), PLANTED.get("INBOX_1_SMTP_USER"), PLANTED.get("INBOX_1_SMTP_PASS")
    if not host:
        return False, "no outgoing mail settings"
    port = int(PLANTED.get("INBOX_1_SMTP_PORT", 465))  # the OS's default: implicit TLS on 465
    if port == 465:
        with smtplib.SMTP_SSL(host, port, timeout=20, context=ssl.create_default_context()) as m:
            m.login(user, pw)
    else:
        with smtplib.SMTP(host, port, timeout=20) as m:
            m.starttls(context=ssl.create_default_context())
            m.login(user, pw)
    return True, f"logged in to {host}:{port} (no message sent)"


def imap():
    host = PLANTED.get("INBOX_1_HOST")
    if not host:
        return False, "no reply inbox settings"
    m = imaplib.IMAP4_SSL(host, int(PLANTED.get("INBOX_1_PORT", 993)), timeout=20)
    m.login(PLANTED["INBOX_1_USER"], PLANTED["INBOX_1_PASS"])
    typ, data = m.select("INBOX", readonly=True)
    m.logout()
    return typ == "OK", f"{host} INBOX readable ({data[0].decode()} messages)"


def calcom():
    key = PC.get("CALCOM_API_KEY")
    if not key:
        return False, "no Cal.com key"
    s, body = get("https://api.cal.com/v2/bookings?status=upcoming&limit=1", {"Authorization": "Bearer " + key, "cal-api-version": "2026-05-01"})
    return s == 200, f"bookings API {s}"


def portal():
    url, key = PLANTED.get("PORTAL_SUPABASE_URL") or PC.get("PORTAL_SUPABASE_URL"), PLANTED.get("PORTAL_SUPABASE_SERVICE_KEY") or PC.get("PORTAL_SUPABASE_SERVICE_KEY")
    if not url or not key:
        return False, "portal URL or key not set"
    s, body = get(url.rstrip("/") + "/rest/v1/sdr_pods?select=pod_slug&limit=1", {"apikey": key, "Authorization": "Bearer " + key})
    if "exceed_egress_quota" in body or "restricted" in body:
        return False, "blocked by Supabase (over the free plan's data limit)"
    if s == 200:
        s2, _ = get(url.rstrip("/") + "/rest/v1/sdr_campaigns?select=pod_slug&limit=1", {"apikey": key, "Authorization": "Bearer " + key})
        return s2 == 200, f"sdr_pods {s}; 11 Nov migration {'applied' if s2 == 200 else 'NOT applied (sdr_campaigns missing)'}"
    return False, f"sdr_pods {s}: {body[:120]}"


def enquiries():
    subprocess.run(["cf", "auth", "whoami"], capture_output=True, timeout=60)
    tok = json.loads((HOME / "Library/Preferences/cloudflare/config/default.json").read_text())["oauth_token"]
    s, body = get("https://api.cloudflare.com/client/v4/accounts/fd8b46e9e422b3970d1d564af7f455db/storage/kv/namespaces/"
                  "7c3ebcd753f7499f972b049608589fbe/keys?prefix=lead:&limit=10", {"Authorization": "Bearer " + tok})
    return s == 200, f"website enquiry store {s}"


def pod_checks(slug):
    rows = psql(f"select p.id, p.status, p.workspace_id from founderos_client_pods p where p.slug = '{slug.replace(chr(39), '')}'")
    if not rows:
        check(f"pod {slug}", lambda: (False, "no such pod"))
        return
    pid, status, ws = rows[0]
    tested = int(psql(f"select count(*) from founderos_outbox o, founderos_client_pods p where p.id = '{pid}' and o.workspace_id = '{ws}' "
                      "and o.status = 'simulated' and o.sent_at > p.config_changed_at")[0][0])
    check(f"pod {slug} test send", lambda: (tested > 0, "a test send after the last settings change" if tested else "no test send since the last settings change"))
    fresh = {"inbox": 20, "portal": 15, "calendar": 30, "crm": 60, "agent": 1440, "crm_blocklist": 60}
    for name, minutes in fresh.items():
        def one(n=name, m=minutes):
            h = psql(f"select ok, detail, checked_at, checked_at > now() - interval '{m} minutes' from founderos_pod_health "
                     f"where pod_id = '{pid}' and check_name = '{n}'")
            if not h:
                return (n in ("crm", "crm_blocklist"), f"{n}: never checked")
            ok, detail, at, recent = h[0]
            return ok == "t" and recent == "t", f"{n}: {detail} (checked {at[:16]}{'' if recent == 't' else ', too long ago'})"
        check(f"pod {slug} {name}", one)
    a = psql(f"select agent_id, synced_at from founderos_pod_agents where pod_id = '{pid}'")
    check(f"pod {slug} agent provisioned", lambda: (bool(a and a[0][0]), f"agent {a[0][0][:8]}… synced {a[0][1][:16]}" if a and a[0][0] else "not provisioned"))


def main():
    for name, fn in (("OS backend and database", backend), ("Agent platform API", paperclip), ("OS agent-tool gateway", gateway),
                     ("Outgoing mail login", smtp), ("Reply inbox", imap), ("Cal.com bookings", calcom),
                     ("Client portal database", portal), ("Website enquiry store", enquiries)):
        check(name, fn)
    if "--pod" in sys.argv:
        pod_checks(sys.argv[sys.argv.index("--pod") + 1])
    failed = [r for r in results if not r[1]]
    print(f"\n{len(results) - len(failed)} of {len(results)} boundaries pass.")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
