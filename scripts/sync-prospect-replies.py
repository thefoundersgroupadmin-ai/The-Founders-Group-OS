#!/usr/bin/env python3
"""Route prospect email replies to the Paperclip sales team.

Reads the INBOX_1 mailbox (read-only IMAP) for messages from the email
domains of pipeline contacts synced from Paperclip (fc-pc-*). For each new
reply it:
  - creates a Paperclip task for Sales Enablement and Delivery to draft a
    response (sending still needs owner approval in the Resend tool), and
  - marks the TFG OS pipeline contact engaged, with an email touch.
Processed Message-IDs are kept in ~/.founderos/state/prospect-replies.json,
so re-runs never create a second task for the same email.

Usage: python3 scripts/sync-prospect-replies.py [--dry-run] [--days N]
"""
import email
import imaplib
import json
import re
import subprocess
import sys
import urllib.request
from datetime import date, datetime, timedelta
from email.header import decode_header, make_header
from email.utils import parseaddr
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
STATE = Path.home() / ".founderos/state/prospect-replies.json"
ASSIGNEE = "Sales Enablement and Delivery"


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
    r = subprocess.run(["psql", db, "-At", "-F", "\t", "-v", "ON_ERROR_STOP=1", "-c", query],
                       capture_output=True, text=True)
    if r.returncode:
        sys.exit("database error: " + r.stderr.strip().splitlines()[0])
    return [line.split("\t") for line in r.stdout.splitlines() if line]


def sql(v):
    return "NULL" if v is None else "'" + str(v).replace("'", "''") + "'"


def text_body(msg):
    parts = msg.walk() if msg.is_multipart() else [msg]
    for p in parts:
        if p.get_content_type() == "text/plain" and not p.get_filename():
            raw = p.get_payload(decode=True) or b""
            return raw.decode(p.get_content_charset() or "utf-8", "replace")
    return ""


def main():
    dry = "--dry-run" in sys.argv
    days = int(sys.argv[sys.argv.index("--days") + 1]) if "--days" in sys.argv else 14
    pc = read_env("~/.founderos/.env")
    keys = read_env("~/.founderos/planted.env")
    db = read_env(REPO / "desktop/backend-go/.env")["DATABASE_URL"]

    contacts = psql(db, "select id, workspace_id, company, email from founderos_funnel_contacts "
                        "where id like 'fc-pc-%' and email is not null")
    domains = {}
    for cid, ws, company, addr in contacts:
        domains[addr.rsplit("@", 1)[1].lower()] = (cid, ws, company)
    if not domains:
        print("no pipeline contacts with an email yet")
        return

    seen = set(json.loads(STATE.read_text())) if STATE.exists() else set()
    since = (date.today() - timedelta(days=days)).strftime("%d-%b-%Y")
    m = imaplib.IMAP4_SSL(keys["INBOX_1_HOST"], int(keys.get("INBOX_1_PORT", 993)), timeout=30)
    m.login(keys["INBOX_1_USER"], keys["INBOX_1_PASS"])
    m.select("INBOX", readonly=True)

    found = []
    for domain, contact in domains.items():
        _, data = m.search(None, f'(SINCE {since} FROM "@{domain}")')
        for num in data[0].split():
            _, parts = m.fetch(num, "(BODY.PEEK[])")
            msg = email.message_from_bytes(parts[0][1])
            mid = (msg.get("Message-ID") or "").strip() or f"{domain}:{num.decode()}"
            sender = parseaddr(msg.get("From", ""))[1].lower()
            if mid in seen or not sender.endswith("@" + domain):
                continue
            found.append((mid, contact, sender, str(make_header(decode_header(msg.get("Subject", "")))),
                          msg.get("Date", ""), text_body(msg)))
    m.logout()

    print(f"{len(found)} new prospect repl{'y' if len(found) == 1 else 'ies'}")
    if dry or not found:
        for mid, (cid, ws, company), sender, subject, when, _ in found:
            print(f"  {company}: {sender} | {subject} | {when}")
        return

    agents = paperclip(pc, "GET", f"/api/companies/{pc['PAPERCLIP_COMPANY_ID']}/agents")
    agents = agents.get("agents", agents) if isinstance(agents, dict) else agents
    assignee = next(a["id"] for a in agents if a["name"] == ASSIGNEE)

    for mid, (cid, ws, company), sender, subject, when, body in found:
        snippet = re.sub(r"\n{3,}", "\n\n", body.strip())[:3000]
        issue = paperclip(pc, "POST", f"/api/companies/{pc['PAPERCLIP_COMPANY_ID']}/issues", {
            "title": f"Prospect reply from {company}: {subject}"[:200],
            "description": (f"A prospect replied to outreach.\n\n- **From:** {sender}\n- **Company:** {company}\n"
                            f"- **Received:** {when}\n- **Subject:** {subject}\n\n**Their message:**\n\n> "
                            + snippet.replace("\n", "\n> ") +
                            "\n\nDraft a reply from Nader (CEO, The Founders Group) and send it with the Resend "
                            "`send-email` tool from hello@thefoundersgroup.net, keeping the opt-out line. Each send "
                            "needs owner approval. If they asked to opt out, add them to suppression and do not reply. "
                            "If they want a call, propose times and note it in the pipeline."),
            "status": "todo", "priority": "high", "workMode": "standard", "assigneeAgentId": assignee})
        psql(db, f"update founderos_funnel_contacts set status='engaged', relationship='warm' "
                 f"where id={sql(cid)} and status in ('first_touch','nurtured')")
        seq = int(psql(db, f"select coalesce(max(seq),0)+1 from founderos_funnel_touches where contact_id={sql(cid)}")[0][0])
        psql(db, "insert into founderos_funnel_touches (id, workspace_id, contact_id, seq, stage, channel, label, source, at) "
                 f"values ({sql(cid + '-reply-' + str(seq))}, {sql(ws)}, {sql(cid)}, {seq}, 'engaged', 'email', "
                 f"{sql('Replied: ' + subject[:180])}, 'manual', {sql(datetime.now().date().isoformat())}) "
                 "on conflict (id) do nothing")
        seen.add(mid)
        STATE.parent.mkdir(parents=True, exist_ok=True)
        STATE.write_text(json.dumps(sorted(seen)))
        print(f"  {company}: task {issue.get('identifier')} created, pipeline → engaged")


if __name__ == "__main__":
    main()
