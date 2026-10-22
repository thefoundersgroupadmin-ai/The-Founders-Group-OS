#!/usr/bin/env python3
"""Move Paperclip agent email drafts into the TFG OS outbox, and report back.

Agents hand over email by writing a task document with key "outbox" that
contains a JSON array (optionally inside a ```json fence) of objects:
  {"to": "...", "toName": "...", "company": "...", "subject": "...", "body": "..."}
Each entry becomes one founderos_outbox row (status pending) with a stable id
derived from the task, recipient and subject, linked to the pipeline contact
with the same email. A recipient with no contact yet gets one (fc-pc-<company>,
Sales System Build, first_touch), so the reply sync watches them and a reply
stops automatic follow-ups. Nothing is sent here: the operator approves in
/os/outbox, and only safe follow-ups send on their own (FOUNDEROS_AUTO_FOLLOWUPS).

Outcomes (sent / rejected / failed / uncertain) are posted back as a comment
on the source task once per attempt, so the agents know what happened; a
retried email is reported again. A lock file keeps two runs from overlapping.

Usage: python3 scripts/sync-paperclip-outbox.py [--dry-run]
"""
import fcntl
import hashlib
import json
import re
import subprocess
import sys
import urllib.request
from email.utils import parseaddr
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
WORKSPACE_SLUG = "founderos"
PIPELINE_SLUG = "vantage"  # displayed as "Sales System Build", as in sync-paperclip-pipeline.py
OWN_DOMAIN = "@thefoundersgroup.net"
EMAIL = re.compile(r"^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$")
FENCE = re.compile(r"```(?:json)?\s*(\[.*?\])\s*```", re.S)


def read_env(path):
    env = {}
    for line in Path(path).expanduser().read_text().splitlines():
        if "=" in line and not line.lstrip().startswith("#"):
            k, v = line.split("=", 1)
            env[k.strip()] = v.strip().strip('"')
    return env


def paperclip(env, method, path, body=None):
    req = urllib.request.Request(env["PAPERCLIP_API_URL"].rstrip("/") + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Authorization": "Bearer " + env["PAPERCLIP_BOARD_KEY"],
                                          "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read().decode(), strict=False)


def psql(db, query):
    r = subprocess.run(["psql", db, "-At", "-v", "ON_ERROR_STOP=1", "-c", query], capture_output=True, text=True)
    if r.returncode:
        sys.exit("database error: " + r.stderr.strip().splitlines()[0])
    return r.stdout


def psql_rows(db, query):
    """Rows as dicts: the query is wrapped in json_agg, so tabs or newlines in values are safe."""
    out = psql(db, f"SELECT coalesce(json_agg(q), '[]') FROM ({query}) q").strip()
    return json.loads(out or "[]")


def slug(s):
    return re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")


def contact_id(it):
    """The pipeline id sync-paperclip-pipeline.py gives this company (or the address's domain)."""
    return "fc-pc-" + slug(it.get("company") or it["to"].rsplit("@", 1)[1])


def is_own(addr, own):
    """Our own mailboxes: test sends to them are not prospects."""
    a = addr.strip().lower()
    return a.endswith(OWN_DOMAIN) or a in own


def sql(v):
    return "NULL" if v in (None, "") else "'" + str(v).replace("'", "''") + "'"


def entries(body):
    m = FENCE.search(body)
    raw = m.group(1) if m else body.strip()
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as e:
        raise ValueError(f"outbox document is not a JSON array: {e}") from None
    if not isinstance(data, list):
        raise ValueError("outbox document must be a JSON array")
    out = []
    for i, e in enumerate(data, 1):
        if not isinstance(e, dict):
            raise ValueError(f"entry {i}: must be an object")
        to = str(e.get("to", "")).strip()
        subject, text = str(e.get("subject", "")).strip(), str(e.get("body", "")).strip()
        if not EMAIL.match(to) or not subject or not text or len(subject) > 300 or len(text) > 20000:
            raise ValueError(f"entry {i}: needs a valid 'to', a subject (1-300) and a body (1-20000)")
        out.append({"to": to, "toName": e.get("toName"), "company": e.get("company"), "subject": subject, "body": text})
    return out


def main():
    dry = "--dry-run" in sys.argv
    pc = read_env("~/.founderos/.env")
    sender = read_env("~/.founderos/planted.env").get("INBOX_1_FROM") or "the TFG OS inbox"
    db = read_env(REPO / "desktop/backend-go/.env")["DATABASE_URL"]
    company = pc["PAPERCLIP_COMPANY_ID"]
    state = Path.home() / ".founderos/state"
    state.mkdir(parents=True, exist_ok=True)
    lock = open(state / "paperclip-outbox.lock", "w")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        print("another outbox sync is running; skipping")
        return
    ws = psql_rows(db, f"select id from workspaces where slug={sql(WORKSPACE_SLUG)}")[0]["id"]
    pipeline_ws = psql_rows(db, f"select id from workspaces where slug={sql(PIPELINE_SLUG)}")[0]["id"]
    keys = read_env("~/.founderos/planted.env")
    own = {parseaddr(keys.get(k, ""))[1].lower() for k in ("INBOX_1_USER", "INBOX_1_FROM")} - {""}

    issues = paperclip(pc, "GET", f"/api/companies/{company}/issues")
    issues = issues.get("issues", issues) if isinstance(issues, dict) else issues
    imported = 0
    for issue in issues:
        docs = paperclip(pc, "GET", f"/api/issues/{issue['id']}/documents")
        docs = docs.get("documents", docs) if isinstance(docs, dict) else docs
        doc = next((d for d in docs if d.get("key") == "outbox"), None)
        if not doc:
            continue
        ident = issue.get("identifier") or issue["id"]
        try:
            items = entries(doc.get("body") or (doc.get("latestRevision") or {}).get("body") or "")
        except ValueError as e:
            print(f"  {ident}: skipped ({e})")
            continue
        for it in items:
            oid = "pc-" + ident.lower() + "-" + hashlib.sha1((it["to"].lower() + "\n" + it["subject"]).encode()).hexdigest()[:10]
            if dry:
                print(f"  {ident}: {it['to']} | {it['subject']}")
                continue
            to, cid = sql(it["to"]), sql(contact_id(it))
            if not is_own(it["to"], own):
                # A new recipient becomes a pipeline contact; a known company keeps its row.
                psql(db, "INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, product, relationship, "
                         "email, person, company, created_at) "
                         f"SELECT {cid}, {sql(pipeline_ws)}, {sql(it['toName'] or it['company'] or it['to'])}, {sql(PIPELINE_SLUG)}, "
                         f"'first_touch', 'Sales System Build', 'cold', {to}, {sql(it['toName'])}, {sql(it['company'])}, now() "
                         f"WHERE NOT EXISTS (SELECT 1 FROM founderos_funnel_contacts WHERE lower(email) = lower({to})) "
                         "ON CONFLICT (id) DO UPDATE SET email = COALESCE(founderos_funnel_contacts.email, EXCLUDED.email)")
            link = (f"COALESCE((SELECT id FROM founderos_funnel_contacts WHERE lower(email) = lower({to}) ORDER BY id LIMIT 1), "
                    f"(SELECT id FROM founderos_funnel_contacts WHERE id = {cid}))")
            # A pending row follows the agent's latest wording; decided rows keep theirs.
            psql(db, "INSERT INTO founderos_outbox (id, workspace_id, source, source_ref, contact_id, to_address, to_name, company, subject, body) "
                     f"SELECT {sql(oid)}, {sql(ws)}, 'paperclip', {sql(ident)}, {link}, "
                     f"{to}, {sql(it['toName'])}, {sql(it['company'])}, {sql(it['subject'])}, {sql(it['body'])} "
                     "ON CONFLICT (id) DO UPDATE SET body = EXCLUDED.body, to_name = EXCLUDED.to_name, company = EXCLUDED.company "
                     "WHERE founderos_outbox.status = 'pending'")
            # Rows imported before their contact existed get linked now, whatever their status.
            psql(db, f"UPDATE founderos_outbox SET contact_id = {link} WHERE id = {sql(oid)} AND contact_id IS NULL")
            imported += 1
    print(f"{imported} outbox entr{'y' if imported == 1 else 'ies'} imported or refreshed")
    if dry:
        return

    by_ident = {i.get("identifier"): i for i in issues}
    done = psql_rows(db, "SELECT id, source_ref, status, attempt, to_address, subject, coalesce(error, '') AS error "
                         "FROM founderos_outbox WHERE source = 'paperclip' AND status IN ('sent', 'rejected', 'failed', 'uncertain') "
                         "AND (reported_attempt IS DISTINCT FROM attempt OR reported_status IS DISTINCT FROM status)")
    for r in done:
        issue = by_ident.get(r["source_ref"])
        if not issue:
            continue
        to, subject = r["to_address"], r["subject"]
        line = {"sent": f"**Sent from TFG OS** ({sender}) to {to}: \"{subject}\".",
                "rejected": f"**Not sent: the owner rejected** the email to {to}: \"{subject}\". Do not resend it as-is.",
                "failed": f"**Send failed** for {to}: \"{subject}\": {r['error']}. The owner can retry from the TFG OS outbox.",
                "uncertain": f"**Delivery uncertain** for {to}: \"{subject}\": {r['error']}. Do not resend; the owner will check.",
                }[r["status"]]
        paperclip(pc, "POST", f"/api/issues/{issue['id']}/comments", {"body": line})
        # Acknowledge only the outcome just reported; a retry since then stays unreported.
        psql(db, f"UPDATE founderos_outbox SET reported_attempt = {int(r['attempt'])}, reported_status = {sql(r['status'])} "
                 f"WHERE id = {sql(r['id'])} AND attempt = {int(r['attempt'])} AND status = {sql(r['status'])}")
        print(f"  reported {r['status']} for {to} on {r['source_ref']}")

if __name__ == "__main__":
    main()
