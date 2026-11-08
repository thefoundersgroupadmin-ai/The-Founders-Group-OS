#!/usr/bin/env python3
"""Route prospect email replies to the Paperclip sales team.

Reads the INBOX_1 mailbox (read-only IMAP) for messages from pipeline contacts
synced from Paperclip (fc-pc-*): any address at a company's domain, or, for
personal mail providers (gmail.com and the like), the contact's own address.
For each new reply it:
  - creates a Paperclip task for Sales Enablement and Delivery to draft a
    response, handed to the TFG OS Outbox for the owner's approval, and
  - marks the TFG OS pipeline contact engaged, with an email touch, which also
    stops automatic follow-ups to them.
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
# Shared mail providers: match the contact's address, never the whole domain.
FREEMAIL = {"gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "live.com", "yahoo.com", "icloud.com",
            "me.com", "aol.com", "proton.me", "protonmail.com", "gmx.com", "zoho.com"}
OWN_DOMAIN = "thefoundersgroup.net"


def watch_keys(contacts, own):
    """What to search the inbox for: {domain-or-address: contact}, skipping our own mailboxes."""
    out = {}
    for cid, ws, company, addr in contacts:
        addr = addr.strip().lower()
        domain = addr.rsplit("@", 1)[1]
        if domain == OWN_DOMAIN or addr in own:
            continue
        out.setdefault(addr if domain in FREEMAIL else domain, (cid, ws, company))
    return out


HQ_CONTACTS_QUERY = ("select id, workspace_id, company, email from founderos_funnel_contacts "
                     "where id like 'fc-pc-%' and email is not null "
                     "and workspace_id NOT IN (SELECT workspace_id FROM founderos_client_pods)")

POD_QUERY = ("select p.slug, p.client_name, p.sender_name, p.workspace_id, a.agent_id, a.project_id "
             "from founderos_client_pods p join founderos_pod_agents a on a.pod_id = p.id "
             "where p.status in ('live','dry_run','paused') and a.agent_id <> '' and a.project_id <> ''")


def pod_contacts_query(workspace_id):
    """A pod's own pipeline contacts with an address: the only ones its mailbox is checked for."""
    return ("select id, workspace_id, company, email from founderos_funnel_contacts "
            f"where workspace_id={sql(workspace_id)} and email is not null")


def reply_contact(contacts, sender):
    """The contact a reply belongs to: the exact sender first, else the first
    contact at the sender's company domain (Codex M1 review #8)."""
    sender = sender.strip().lower()
    exact = next((c for c in contacts if c[3].strip().lower() == sender), None)
    if exact:
        return exact
    domain = sender.rsplit("@", 1)[-1]
    if domain in FREEMAIL:
        return None
    return next((c for c in contacts if c[3].strip().lower().rsplit("@", 1)[-1] == domain), None)


def company_contacts(contacts, sender):
    """Everyone at the replying company: one reply stops follow-ups to all of them."""
    domain = sender.strip().lower().rsplit("@", 1)[-1]
    if domain in FREEMAIL:
        return [c[0] for c in contacts if c[3].strip().lower() == sender.strip().lower()]
    return [c[0] for c in contacts if c[3].strip().lower().rsplit("@", 1)[-1] == domain]


def pod_mailbox(env, slug):
    """The pod's own IMAP login from POD_<SLUG>_* keys (as TFG OS saves them), or None."""
    pre = "POD_" + slug.upper().replace("-", "_") + "_"
    host, user, pw = env.get(pre + "HOST"), env.get(pre + "USER"), env.get(pre + "PASS")
    if not (host and user and pw):
        return None
    return {"host": host, "port": int(env.get(pre + "PORT") or 993), "user": user, "pass": pw}


OPT_OUT = re.compile(r"\b(no thanks|no, thanks|unsubscribe|not interested|remove me|take me off|opt[- ]?out|"
                     r"do not (contact|email)|don'?t (contact|email))\b", re.I)


def is_opt_out(body):
    """Whether a reply asks us to stop. Only the reply itself counts, not the quoted email below it."""
    text = re.split(r"\n\s*(>|On .{0,80}wrote:)", body or "", maxsplit=1)[0].strip()
    first = text.splitlines()[0] if text else ""
    return bool(OPT_OUT.search(text[:600]) or re.fullmatch(r"\s*(stop|unsubscribe)\W*", first, re.I))


HUMAN = re.compile(r"\b(call|phone|speak|talk|human|person|someone|meeting|meet)\b", re.I)


def pod_reply_task(pod, sender, company, subject, when, body):
    """The task for the pod's own AI SDR: this pod's context only, signed by its sender."""
    snippet = re.sub(r"\n{3,}", "\n\n", (body or "").strip())[:3000]
    wants_person = bool(HUMAN.search(snippet[:800]))
    return {
        "title": f"{pod['client_name']}: reply from {company}: {subject}"[:200],
        "description": (f"A prospect replied to {pod['client_name']}'s outreach.\n\n- **From:** {sender}\n"
                        f"- **Company:** {company}\n- **Received:** {when}\n- **Subject:** {subject}\n\n"
                        "**Their message:**\n\n> " + snippet.replace("\n", "\n> ") + "\n\n"
                        f"Draft a reply from {pod['sender_name']} and hand it over as a document with key \"outbox\" on "
                        "this task: a JSON array of {\"to\", \"toName\", \"company\", \"subject\", \"body\"}. "
                        "TFG OS sends it only after its checks and the client's approval; never send any other way. "
                        "Automatic follow-ups to them have stopped. Work only from this task and your instructions; "
                        "use no other client's or company's data.\n\n"
                        + ("They may want to talk to a person: also write a document with key \"handoff\" for "
                           f"{pod['client_name']}'s rep: who they are, what they asked, what they need, and suggested next "
                           "steps, so a human can take over fully briefed." if wants_person else
                           "If they ask to speak to a person, also write a \"handoff\" document for the rep.")),
        "status": "todo", "priority": "high", "workMode": "standard",
        "assigneeAgentId": pod["agent_id"], "projectId": pod["project_id"],
    }


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


def hq_pass():
    """Replies to TFG's own outreach, in TFG's inbox (never a client pod's contacts)."""
    dry = "--dry-run" in sys.argv
    days = int(sys.argv[sys.argv.index("--days") + 1]) if "--days" in sys.argv else 14
    pc = read_env("~/.founderos/.env")
    keys = read_env("~/.founderos/planted.env")
    db = read_env(REPO / "desktop/backend-go/.env")["DATABASE_URL"]

    contacts = psql(db, HQ_CONTACTS_QUERY)
    own = {parseaddr(keys.get(k, ""))[1].lower() for k in ("INBOX_1_USER", "INBOX_1_FROM")} - {""}
    domains = watch_keys(contacts, own)
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
        whole = "@" in domain  # a personal address, not a company domain
        _, data = m.search(None, f'(SINCE {since} FROM "{domain if whole else "@" + domain}")')
        for num in data[0].split():
            _, parts = m.fetch(num, "(BODY.PEEK[])")
            msg = email.message_from_bytes(parts[0][1])
            mid = (msg.get("Message-ID") or "").strip() or f"{domain}:{num.decode()}"
            sender = parseaddr(msg.get("From", ""))[1].lower()
            if mid in seen or not (sender == domain if whole else sender.endswith("@" + domain)):
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
        # These workers cannot read arbitrary Mac paths. Attach the same
        # authorised OS context used by Conductor tasks before waking one.
        packet = subprocess.run([sys.executable, str(REPO / "scripts/tfg-agent-context.py"), "--workspace", "founderos"], capture_output=True, text=True)
        if packet.returncode:
            print("Reply retained: the approved OS context packet is unavailable.")
            return
        snippet = re.sub(r"\n{3,}", "\n\n", body.strip())[:3000]
        issue = paperclip(pc, "POST", f"/api/companies/{pc['PAPERCLIP_COMPANY_ID']}/issues", {
            "title": f"Prospect reply from {company}: {subject}"[:200],
            "description": (f"A prospect replied to outreach.\n\n- **From:** {sender}\n- **Company:** {company}\n"
                            f"- **Received:** {when}\n- **Subject:** {subject}\n\n**Their message:**\n\n> "
                            + snippet.replace("\n", "\n> ") +
                            "\n\nDraft a reply from Nader (CEO, The Founders Group) and hand it over as a document with "
                            "key \"outbox\" on this task: a JSON array of {\"to\", \"toName\", \"company\", \"subject\", "
                            "\"body\"}. The owner approves it in the TFG OS Outbox; never send any other way. Automatic "
                            "follow-ups to them have stopped. If they asked to opt out, do not reply and note it in the "
                            "pipeline. If they want a call, propose times and note it in the pipeline."
                            "\n\n# Verified OS execution context and attached owner handover\n"
                            "Use this supplied profile, handover and workflow catalogue; no local filesystem reads are required. "
                            "Save an internal report document. Unknown optional facts are report gaps, not permission to invent them.\n" + packet.stdout),
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


POD_STATE = Path.home() / ".founderos/state/pod-replies.json"


def search_replies(mailbox, domains, since, seen):
    """New messages from watched domains/addresses in one mailbox."""
    m = imaplib.IMAP4_SSL(mailbox["host"], mailbox["port"], timeout=30)
    m.login(mailbox["user"], mailbox["pass"])
    m.select("INBOX", readonly=True)
    found = []
    for domain, contact in domains.items():
        whole = "@" in domain
        _, data = m.search(None, f'(SINCE {since} FROM "{domain if whole else "@" + domain}")')
        for num in data[0].split():
            _, parts = m.fetch(num, "(BODY.PEEK[])")
            msg = email.message_from_bytes(parts[0][1])
            mid = (msg.get("Message-ID") or "").strip() or f"{domain}:{num.decode()}"
            sender = parseaddr(msg.get("From", ""))[1].lower()
            if mid in seen or not (sender == domain if whole else sender.endswith("@" + domain)):
                continue
            found.append((mid, contact, sender, str(make_header(decode_header(msg.get("Subject", "")))),
                          msg.get("Date", ""), text_body(msg)))
    m.logout()
    return found


def pod_pass():
    """Replies to each client pod's outreach, in that pod's own mailbox, handled
    with that pod's context only (docs/tfg/CLIENT-PODS.md)."""
    dry = "--dry-run" in sys.argv
    days = int(sys.argv[sys.argv.index("--days") + 1]) if "--days" in sys.argv else 14
    pc = read_env("~/.founderos/.env")
    planted = Path("~/.founderos/planted.env").expanduser()
    env = {**pc, **(read_env(planted) if planted.exists() else {})}
    db = read_env(REPO / "desktop/backend-go/.env")["DATABASE_URL"]
    since = (date.today() - timedelta(days=days)).strftime("%d-%b-%Y")
    seen = set(json.loads(POD_STATE.read_text())) if POD_STATE.exists() else set()
    for slug, client, sender_name, ws, agent_id, project_id in psql(db, POD_QUERY):
        pod = {"slug": slug, "client_name": client, "sender_name": sender_name, "workspace_id": ws,
               "agent_id": agent_id, "project_id": project_id}
        box = pod_mailbox(env, slug)
        if not box:
            print(f"{slug}: no mailbox configured; replies not checked")
            continue
        pod_contacts = psql(db, pod_contacts_query(ws))
        domains = watch_keys(pod_contacts, {box["user"].lower()})
        if not domains:
            continue
        try:
            found = search_replies(box, domains, since, seen)
        except (imaplib.IMAP4.error, OSError) as e:
            print(f"{slug}: mailbox unreadable ({e}); will retry")
            continue
        print(f"{slug}: {len(found)} new repl{'y' if len(found) == 1 else 'ies'}")
        for mid, (cid, cws, company), sender, subject, when, body in found:
            exact = reply_contact(pod_contacts, sender)
            if exact:
                cid, company = exact[0], exact[2] or company
            if dry:
                print(f"  {company}: {sender} | {subject}")
                continue
            if is_opt_out(body):
                # Under the pod send gate's lock, so a send in flight finishes first and none follows.
                psql(db, f"select pg_advisory_xact_lock(hashtext('founderos-pod-send:' || {sql(ws)})); "
                         "insert into founderos_email_suppression (workspace_id, address, reason) "
                         f"values ({sql(ws)}, {sql(sender)}, {sql('opted out by reply: ' + subject[:120])}) on conflict do nothing")
                psql(db, f"update founderos_outbox set status='rejected', decided_at=now(), error='opted out by reply' "
                         f"where workspace_id={sql(ws)} and lower(to_address)={sql(sender)} and status='pending'")
                psql(db, "insert into founderos_client_activity (pod_id, kind, summary, ref) "
                         f"select id, 'opted_out', {sql(company + ' asked not to be contacted again')}, {sql(cid)} "
                         f"from founderos_client_pods where slug={sql(slug)}")
            else:
                issue = paperclip(pc, "POST", f"/api/companies/{pc['PAPERCLIP_COMPANY_ID']}/issues",
                                  pod_reply_task(pod, sender, company, subject, when, body))
                psql(db, "insert into founderos_pod_conversations (issue_id, workspace_id, contact_id) "
                         f"values ({sql(issue['id'])}, {sql(ws)}, {sql(cid)}) on conflict do nothing")
                kind = "handoff_requested" if HUMAN.search((body or "")[:800]) else "reply_received"
                psql(db, "insert into founderos_client_activity (pod_id, kind, summary, ref) "
                         f"select id, {sql(kind)}, {sql(company + ' replied: ' + subject[:140])}, {sql(issue.get('identifier') or cid)} "
                         f"from founderos_client_pods where slug={sql(slug)}")
            # One reply stops follow-ups to everyone at that company in this pod.
            ids = company_contacts(pod_contacts, sender) or [cid]
            psql(db, "update founderos_funnel_contacts set status='engaged', relationship='warm' "
                     f"where workspace_id={sql(ws)} and id in ({', '.join(sql(i) for i in ids)}) and status in ('first_touch','nurtured')")
            seq = int(psql(db, f"select coalesce(max(seq),0)+1 from founderos_funnel_touches where contact_id={sql(cid)}")[0][0])
            psql(db, "insert into founderos_funnel_touches (id, workspace_id, contact_id, seq, stage, channel, label, source, at) "
                     f"values ({sql(cid + '-reply-' + str(seq))}, {sql(ws)}, {sql(cid)}, {seq}, 'engaged', 'email', "
                     f"{sql(('Opted out: ' if is_opt_out(body) else 'Replied: ') + subject[:170])}, 'manual', "
                     f"{sql(datetime.now().date().isoformat())}) on conflict (id) do nothing")
            seen.add(mid)
            POD_STATE.parent.mkdir(parents=True, exist_ok=True)
            POD_STATE.write_text(json.dumps(sorted(seen)))


def main():
    try:
        hq_pass()
    except SystemExit as e:
        if e.code not in (None, 0):
            print(f"TFG inbox pass stopped: {e.code}")
    pod_pass()


if __name__ == "__main__":
    main()
