#!/usr/bin/env python3
"""Brings website enquiries into TFG OS.

The website (Cloudflare Worker tfg-site) saves every enquiry, quote download and
"proceed with this quote" in its KV namespace as lead:<iso time>:<id>. Every five
minutes (launchd net.thefoundersgroup.website-enquiries) this script reads the new
ones through the Cloudflare API with the owner's existing login on this Mac (the
cf CLI's OAuth token), adds each person to the TFG pipeline (Sales System Build
workspace, like the research import) with a touch row, and sends the owner a
phone notification through the doctor's notice queue. It also warns when an
enquiry could not be passed to the CRM after every retry.

  sync-website-enquiries.py            import new enquiries
  sync-website-enquiries.py --dry-run  show what would be imported
"""
import json
import re
import subprocess
import sys
import urllib.parse
import urllib.request
from pathlib import Path

HOME = Path.home()
REPO = Path(__file__).resolve().parent.parent
ACCOUNT = "fd8b46e9e422b3970d1d564af7f455db"
NAMESPACE = "7c3ebcd753f7499f972b049608589fbe"
STATE = HOME / ".founderos/state/website-enquiries.json"
NOTIFY = HOME / ".founderos/doctor/notify"
CF_CONFIG = HOME / "Library/Preferences/cloudflare/config/default.json"
WORKSPACE_SLUG = "vantage"  # "Sales System Build": where TFG's own pipeline lives

KINDS = {"contact": "Website enquiry", "quote": "Quote downloaded", "quote-accept": "Wants to proceed with a quote"}


def sql(v):
    if v is None:
        return "NULL"
    if isinstance(v, (int, float)):
        return str(v)
    return "'" + str(v).replace("'", "''") + "'"


def read_env(path):
    env = {}
    for line in Path(path).read_text().splitlines():
        if "=" in line and not line.lstrip().startswith("#"):
            k, v = line.split("=", 1)
            env[k.strip()] = v.strip().strip('"')
    return env


def token():
    subprocess.run(["cf", "auth", "whoami"], capture_output=True, timeout=60)  # refreshes the short-lived token
    return json.loads(CF_CONFIG.read_text())["oauth_token"]


def cf(path, tok):
    req = urllib.request.Request("https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s/%s" % (ACCOUNT, NAMESPACE, path),
                                 headers={"Authorization": "Bearer " + tok})
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.read()


def list_keys(tok):
    keys, cursor = [], ""
    while True:
        q = "keys?prefix=lead:&limit=1000" + ("&cursor=" + urllib.parse.quote(cursor) if cursor else "")
        d = json.loads(cf(q, tok))
        keys += [k["name"] for k in d.get("result", [])]
        cursor = (d.get("result_info") or {}).get("cursor") or ""
        if not cursor:
            return sorted(keys)


def contact_id(email):
    return "fc-web-" + re.sub(r"[^a-z0-9]+", "-", email.lower()).strip("-")[:80]


def plan(lead):
    """What one enquiry becomes: (contact fields, touch label, notification title, body)."""
    kind = lead.get("form-name") or "contact"
    what = KINDS.get(kind, "Website enquiry")
    name = (lead.get("name") or "").strip() or lead["email"]
    company = (lead.get("company") or "").strip()
    bits = []
    if kind == "contact":
        if lead.get("team"):
            bits.append("Sales team: " + lead["team"])
        if lead.get("message"):
            bits.append(lead["message"])
        if lead.get("assessment"):
            bits.append("Assessment: " + lead["assessment"])
    else:
        bits.append("%s, %s reps, quote %s: %s then %s a month (valid until %s)" % (
            lead.get("package", "?"), lead.get("reps", "?"), lead.get("quoteRef", "?"),
            lead.get("quoteMonthOne", "?"), lead.get("quoteMonthly", "?"), lead.get("quoteValidUntil", "?")))
    detail = " · ".join(bits)
    nxt = {"contact": "Reply within one working day; check whether they booked a call",
           "quote": "Follow up on the quote they downloaded",
           "quote-accept": "Send the services agreement and book the kickoff call"}.get(kind, "Reply within one working day")
    contact = {"id": contact_id(lead["email"]), "name": name, "person": name if lead.get("name") else None,
               "company": company or None, "email": lead["email"].strip().lower(),
               "relationship": "hot" if kind == "quote-accept" else "warm",
               "likelihood": 70 if kind == "quote-accept" else 50, "status": "engaged",
               "evidence": ("%s on %s. %s" % (what, lead.get("received", "")[:10], detail)).strip(),
               "next": nxt}
    title = "%s: %s%s" % (what, name, (", " + company) if company else "")
    return contact, "%s (%s)" % (what, lead.get("page") or "website"), title, detail[:280] or lead["email"]


def statements(ws, key, lead):
    c, touch, _, _ = plan(lead)
    tid = c["id"] + "-" + key.rsplit(":", 1)[-1]
    return [
        "INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, relationship, likelihood, email, person, company, "
        "research_evidence, next_action, research_source_ref, created_at) VALUES (%s, %s, %s, 'vantage', %s, %s, %s, %s, %s, %s, %s, %s, 'website', %s) "
        "ON CONFLICT (id) DO UPDATE SET status = CASE WHEN founderos_funnel_contacts.status = 'first_touch' THEN 'engaged' ELSE founderos_funnel_contacts.status END, "
        "relationship = CASE WHEN EXCLUDED.relationship = 'hot' THEN 'hot' ELSE founderos_funnel_contacts.relationship END, "
        "likelihood = GREATEST(founderos_funnel_contacts.likelihood, EXCLUDED.likelihood), "
        "person = COALESCE(founderos_funnel_contacts.person, EXCLUDED.person), company = COALESCE(founderos_funnel_contacts.company, EXCLUDED.company), "
        "research_evidence = EXCLUDED.research_evidence || COALESCE(E'\\n' || founderos_funnel_contacts.research_evidence, ''), "
        "next_action = EXCLUDED.next_action;" % (
            sql(c["id"]), sql(ws), sql(c["name"]), sql(c["status"]), sql(c["relationship"]), c["likelihood"], sql(c["email"]),
            sql(c["person"]), sql(c["company"]), sql(c["evidence"]), sql(c["next"]), sql(lead.get("received") or "now()")),
        "INSERT INTO founderos_funnel_touches (id, workspace_id, contact_id, seq, stage, channel, label, source, at) VALUES "
        "(%s, %s, %s, (SELECT COALESCE(max(seq), 0) + 1 FROM founderos_funnel_touches WHERE contact_id = %s), 'engaged', 'organic', %s, 'manual', %s) "
        "ON CONFLICT (id) DO NOTHING;" % (sql(tid), sql(ws), sql(c["id"]), sql(c["id"]), sql(touch), sql((lead.get("received") or "")[:10] or None)),
    ]


def notice(nid, title, body):
    NOTIFY.mkdir(parents=True, exist_ok=True)
    safe = re.sub(r"[^A-Za-z0-9_.-]", "-", nid)[:120]
    (NOTIFY / (safe + ".json")).write_text(json.dumps({"id": safe, "kind": "reply", "title": title[:120], "body": body[:300], "url": "/os/funnel"}))


def main():
    dry = "--dry-run" in sys.argv
    state = json.loads(STATE.read_text()) if STATE.exists() else {"after": "", "warned": []}
    state.setdefault("forwarding", [])  # imported enquiries still waiting to reach the CRM
    tok = token()
    keys = [k for k in list_keys(tok) if k > state.get("after", "")]
    db = read_env(REPO / "desktop/backend-go/.env")["DATABASE_URL"]
    ws = subprocess.run(["psql", db, "-At", "-c", "select id from workspaces where slug = %s" % sql(WORKSPACE_SLUG)],
                        capture_output=True, text=True, check=True).stdout.strip()
    if not ws:
        sys.exit("workspace %s not found" % WORKSPACE_SLUG)
    done = 0
    for key in keys:
        try:
            lead = json.loads(cf("values/" + urllib.parse.quote(key, safe=""), tok))
        except Exception as e:
            print("skip", key, e)
            break  # keep order: retry from here next run
        if not re.match(r"^[^\s@]+@[^\s@]+\.[^\s@]+$", lead.get("email", "")):
            state["after"] = key
            continue
        _, _, title, body = plan(lead)
        if dry:
            print(key, "->", title, "|", body)
            continue
        sql_text = "BEGIN;\n" + "\n".join(statements(ws, key, lead)) + "\nCOMMIT;"
        r = subprocess.run(["psql", db, "-v", "ON_ERROR_STOP=1", "-q"], input=sql_text, capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit("import failed at %s: %s" % (key, r.stderr.strip()[:300]))
        notice("web-" + key.rsplit(":", 1)[-1], title, body)
        state["after"] = key
        done += 1
        if not (lead.get("forward") or {}).get("ok"):
            state["forwarding"].append(key)
    # Enquiries the website hasn't passed to the CRM yet are re-read until they get through or the
    # Worker gives up after 10 tries, which is when the owner hears about it.
    # Oldest first, 200 a run; whatever isn't reached this run stays queued for the next.
    batch, rest = state["forwarding"][:200], state["forwarding"][200:]
    still = []
    for key in batch:
        try:
            lead = json.loads(cf("values/" + urllib.parse.quote(key, safe=""), tok))
        except Exception:
            still.append(key)
            continue
        fwd = lead.get("forward") or {}
        if fwd.get("ok"):
            continue
        if fwd.get("tries", 0) >= 10:
            if key not in state["warned"] and not dry:
                notice("web-crm-" + key.rsplit(":", 1)[-1], "Enquiry not in the CRM",
                       "%s couldn't be passed to the CRM after 10 tries (%s). It is in TFG OS." % (lead.get("email", ""), fwd.get("error", "")))
                state["warned"] = (state["warned"] + [key])[-200:]
            continue
        still.append(key)
    state["forwarding"] = rest + still  # checked ones go to the back, so every entry gets its turn
    if not dry:
        STATE.parent.mkdir(parents=True, exist_ok=True)
        STATE.write_text(json.dumps(state))
    print("%d new enquiries imported" % done)


if __name__ == "__main__":
    main()
