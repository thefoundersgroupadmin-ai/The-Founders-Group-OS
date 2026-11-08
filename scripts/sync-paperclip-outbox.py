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
FENCE_OBJ = re.compile(r"```(?:json)?\s*(\{.*?\})\s*```", re.S)


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


def imported_pipeline_query(oid):
    """The pipeline of the contact an earlier import linked this draft to.
    Outbox rows all live in HQ, so the row's own workspace says nothing."""
    return ("SELECT c.workspace_id::text AS workspace_id,w.slug FROM founderos_outbox o "
            "JOIN founderos_funnel_contacts c ON c.id=o.contact_id JOIN workspaces w ON w.id=c.workspace_id "
            f"WHERE o.id={sql(oid)}")


def recipient_workspace(matches, existing):
    """The pipeline a draft belongs to: the one its contact is in, or, when the
    contact is in several, the pipeline of the contact an earlier import
    linked the draft to (never the Outbox row's own workspace, which is HQ). None means
    no contact yet (use the default); ambiguity with no earlier import is
    returned as the string 'ambiguous'."""
    if existing:
        return existing[0]
    if len(matches) > 1:
        return "ambiguous"
    return matches[0] if matches else None


POD_ROUTES_QUERY = ("SELECT a.project_id, p.workspace_id::text AS workspace_id, p.slug, p.client_name, p.sender_name "
                    "FROM founderos_pod_agents a JOIN founderos_client_pods p ON p.id=a.pod_id WHERE a.project_id<>''")


def route(issue, pods, task_workspaces, default_pipeline_ws):
    """Where an issue's drafts go. A task in a client pod's board project goes to
    that pod's workspace and pipeline, whatever else matches (docs/tfg/CLIENT-PODS.md).
    Otherwise the durable command binding, else the default pipeline; outbox_ws
    None means HQ."""
    pod = pods.get(issue.get("projectId") or "")
    if pod:
        return {"pod": True, "outbox_ws": pod["workspace_id"], "pipeline_ws": pod["workspace_id"], "pipeline_slug": pod["slug"],
                "venture": "client", "product": pod["client_name"], "sender": pod["sender_name"], "binding": True}
    binding = task_workspaces.get(issue["id"])
    return {"pod": False, "outbox_ws": None, "pipeline_ws": binding["workspace_id"] if binding else default_pipeline_ws,
            "pipeline_slug": binding["slug"] if binding else PIPELINE_SLUG, "venture": None, "product": "Sales System Build",
            "sender": None, "binding": bool(binding)}


def outbox_id(ident, it, pod_slug):
    """A draft's stable id. Pod drafts carry the pod's slug (Codex M1 review #7), so a
    task moved to another pod never rewrites the first pod's draft; HQ ids are unchanged."""
    h = hashlib.sha1((it["to"].lower() + "\n" + it["subject"]).encode()).hexdigest()[:10]
    return "pc-" + (pod_slug + "-" if pod_slug else "") + ident.lower() + "-" + h


# A re-import refreshes a pending draft's wording only inside its own workspace.
OUTBOX_UPSERT_GUARD = ("WHERE founderos_outbox.status = 'pending' "
                       "AND founderos_outbox.workspace_id = EXCLUDED.workspace_id")

CONVERSATIONS_QUERY = "SELECT issue_id FROM founderos_pod_conversations"


def sequence_key_for(entry, is_reply_task):
    """Drafts on a prospect-reply task are conversation replies, whatever the agent wrote."""
    return "reply" if is_reply_task else entry.get("sequenceKey")


def quarantined(issue, pods, project_names):
    """A task in a client board project that no pod owns goes nowhere: never into
    TFG's pipeline or outbox (Codex reviews #6). A client project is named
    "Client: ..." or carries the "(TFG OS pod ...)" description provisioning
    writes, so a renamed or unbound pod project is still recognised."""
    pid = issue.get("projectId") or ""
    meta = project_names.get(pid) or ""
    return bool(pid) and pid not in pods and (meta.startswith("Client:") or "(TFG OS pod " in meta)


def project_names_of(projects):
    """Project id -> name and description, from the board (a failed read raises: fail closed)."""
    projects = projects.get("projects", projects) if isinstance(projects, dict) else projects
    return {p["id"]: (p.get("name") or "") + "\n" + (p.get("description") or "")
            for p in projects or [] if isinstance(p, dict) and p.get("id")}


def withdraw_all_query(ident):
    """Every waiting draft of a quarantined task."""
    return ("UPDATE founderos_outbox SET status = 'rejected', decided_at = now(), error = 'task is in a client project with no pod' "
            f"WHERE source = 'paperclip' AND source_ref = {sql(ident)} AND status = 'pending'")


def withdraw_moved_query(ident, workspace_id):
    """Waiting drafts a task left in another workspace when it moved (Codex fix review #7)."""
    return ("UPDATE founderos_outbox SET status = 'rejected', decided_at = now(), error = 'task moved to another workspace' "
            f"WHERE source = 'paperclip' AND source_ref = {sql(ident)} AND status = 'pending' AND workspace_id <> {sql(workspace_id)}")


def handoff_query(workspace_id, issue_id, body):
    """A buyer ready for a person: queued for the client's rep (TFG OS emails it)."""
    return ("INSERT INTO founderos_pod_handoffs (pod_id, issue_id, contact_id, summary) "
            f"SELECT p.id, {sql(issue_id)}, (SELECT contact_id FROM founderos_pod_conversations WHERE issue_id = {sql(issue_id)}), "
            f"{sql(body.strip()[:4000])} FROM founderos_client_pods p WHERE p.workspace_id = {sql(workspace_id)} "
            "ON CONFLICT (issue_id) DO NOTHING")


def qualification_update(workspace_id, issue_id, body):
    """What the agent learned (budget, timing, who decides, fit) onto the lead's pipeline record."""
    m = FENCE_OBJ.search(body or "")
    try:
        q = json.loads(m.group(1) if m else (body or "").strip())
    except json.JSONDecodeError:
        return None
    if not isinstance(q, dict):
        return None
    try:
        fit = max(0, min(100, int(q.get("fit")))) if q.get("fit") not in (None, "") else None
    except (TypeError, ValueError):
        fit = None
    parts = [f"{label}: {str(q.get(key)).strip()[:200]}" for key, label in
             (("budget", "Budget"), ("timing", "Timing"), ("decisionMaker", "Decides"), ("notes", "Notes")) if q.get(key)]
    sets = [f"next_action = {sql(('Qualified. ' + '; '.join(parts))[:900])}"] if parts else []
    if fit is not None:
        sets.append(f"likelihood = {fit}")
    if not sets:
        return None
    return (f"UPDATE founderos_funnel_contacts SET {', '.join(sets)} WHERE workspace_id = {sql(workspace_id)} "
            f"AND id = (SELECT contact_id FROM founderos_pod_conversations WHERE issue_id = {sql(issue_id)})")


def brief_query(issue_id, body):
    """A call brief for the meeting this task was opened for (until it has been sent)."""
    return (f"UPDATE founderos_pod_meetings SET brief = {sql(body.strip()[:8000])} "
            f"WHERE brief_issue_id = {sql(issue_id)} AND brief_sent_at IS NULL")


def recipient_matches_query(addr):
    """HQ drafts with no command binding find their pipeline by recipient, never inside a client pod."""
    return ("SELECT DISTINCT c.workspace_id::text AS workspace_id,w.slug FROM founderos_funnel_contacts c JOIN workspaces w ON w.id=c.workspace_id "
            f"WHERE lower(c.email)=lower({sql(addr)}) AND c.workspace_id NOT IN (SELECT workspace_id FROM founderos_client_pods)")


def psql_rows(db, query):
    """Rows as dicts: the query is wrapped in json_agg, so tabs or newlines in values are safe."""
    out = psql(db, f"SELECT coalesce(json_agg(q), '[]') FROM ({query}) q").strip()
    return json.loads(out or "[]")


def slug(s):
    return re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")


def contact_id(it, workspace=PIPELINE_SLUG):
    """The pipeline id sync-paperclip-pipeline.py gives this company (or the address's domain)."""
    return "fc-pc-" + (slug(workspace) + "-" if workspace != PIPELINE_SLUG else "") + slug(it.get("company") or it["to"].rsplit("@", 1)[1])


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
        seq = e.get("sequenceKey")
        if seq is not None and (not isinstance(seq, str) or not 0 < len(seq.strip()) <= 100):
            raise ValueError(f"entry {i}: sequenceKey must be a short string")
        out.append({"to": to, "toName": e.get("toName"), "company": e.get("company"), "subject": subject, "body": text,
                    "sequenceKey": seq.strip() if seq else None})
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
    # Route task output to the durable command's workspace, never a workspace
    # named by model-generated document text.
    bindings = psql_rows(db, "SELECT step->>'taskId' AS task_id,w.id::text AS workspace_id,w.slug FROM founderos_commands c JOIN workspaces w ON w.id=c.workspace_id CROSS JOIN LATERAL jsonb_array_elements(c.steps) step WHERE step->>'taskId' IS NOT NULL")
    task_workspaces = {b["task_id"]: b for b in bindings}
    default_pipeline_ws = pipeline_ws
    imported = 0
    pods = {p["project_id"]: p for p in psql_rows(db, POD_ROUTES_QUERY)}
    conversations = {r["issue_id"] for r in psql_rows(db, CONVERSATIONS_QUERY)}
    pod_senders = {p["workspace_id"]: p["sender_name"] for p in pods.values()}
    project_names = project_names_of(paperclip(pc, "GET", f"/api/companies/{company}/projects"))
    for issue in issues:
        qident = issue.get("identifier") or issue["id"]
        if quarantined(issue, pods, project_names):
            print(f"  {qident}: skipped (client project with no pod)")
            if not dry:
                psql(db, withdraw_all_query(qident))
            continue
        rt = route(issue, pods, task_workspaces, default_pipeline_ws)
        if not dry:
            # Before reading documents: a task that moved loses its old drafts even if it has no outbox yet.
            psql(db, withdraw_moved_query(qident, rt["outbox_ws"] or ws))
        binding = rt["binding"]
        pipeline_ws, pipeline_slug = rt["pipeline_ws"], rt["pipeline_slug"]
        outbox_ws = rt["outbox_ws"] or ws
        docs = paperclip(pc, "GET", f"/api/issues/{issue['id']}/documents")
        docs = docs.get("documents", docs) if isinstance(docs, dict) else docs
        if rt["pod"] and not dry:
            for d in docs:
                text = d.get("body") or (d.get("latestRevision") or {}).get("body") or ""
                if d.get("key") == "handoff" and text.strip():
                    psql(db, handoff_query(rt["outbox_ws"], issue["id"], text))
                elif d.get("key") == "brief" and text.strip():
                    psql(db, brief_query(issue["id"], text))
                elif d.get("key") == "qualification":
                    upd = qualification_update(rt["outbox_ws"], issue["id"], text)
                    if upd:
                        psql(db, upd)
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
            oid = outbox_id(ident, it, rt["pipeline_slug"] if rt["pod"] else None)
            if not binding:
                matches = psql_rows(db, recipient_matches_query(it["to"]))
                existing = psql_rows(db, imported_pipeline_query(oid)) if len(matches) > 1 else []
                chosen = recipient_workspace(matches, existing)
                if chosen == "ambiguous":
                    print(f"  {ident}: skipped draft with ambiguous recipient workspace")
                    continue
                pipeline_ws = chosen["workspace_id"] if chosen else default_pipeline_ws
                pipeline_slug = chosen["slug"] if chosen else PIPELINE_SLUG
            if dry:
                print(f"  {ident}: {it['to']} | {it['subject']}")
                continue
            to, cid = sql(it["to"]), sql(contact_id(it, pipeline_slug))
            if not is_own(it["to"], own):
                # A new recipient becomes a pipeline contact; a known company keeps its row.
                psql(db, "INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, product, relationship, "
                         "email, person, company, created_at) "
                         f"SELECT {cid}, {sql(pipeline_ws)}, {sql(it['toName'] or it['company'] or it['to'])}, {sql(rt['venture'] or pipeline_slug)}, "
                         f"'first_touch', {sql(rt['product'])}, 'cold', {to}, {sql(it['toName'])}, {sql(it['company'])}, now() "
                         f"WHERE NOT EXISTS (SELECT 1 FROM founderos_funnel_contacts WHERE workspace_id = {sql(pipeline_ws)} AND lower(email) = lower({to})) "
                         "ON CONFLICT (id) DO UPDATE SET email = COALESCE(founderos_funnel_contacts.email, EXCLUDED.email)")
            link = (f"COALESCE((SELECT id FROM founderos_funnel_contacts WHERE workspace_id = {sql(pipeline_ws)} AND lower(email) = lower({to}) ORDER BY id LIMIT 1), "
                    f"(SELECT id FROM founderos_funnel_contacts WHERE workspace_id = {sql(pipeline_ws)} AND id = {cid}))")
            # A pending row follows the agent's latest wording; decided rows keep theirs.
            psql(db, "INSERT INTO founderos_outbox (id, workspace_id, source, source_ref, contact_id, to_address, to_name, company, subject, body, sequence_key) "
                     f"SELECT {sql(oid)}, {sql(outbox_ws)}, 'paperclip', {sql(ident)}, {link}, "
                     f"{to}, {sql(it['toName'])}, {sql(it['company'])}, {sql(it['subject'])}, {sql(it['body'])}, "
                     f"{sql(sequence_key_for(it, issue['id'] in conversations))} "
                     "ON CONFLICT (id) DO UPDATE SET body = EXCLUDED.body, to_name = EXCLUDED.to_name, company = EXCLUDED.company, "
                     "sequence_key = EXCLUDED.sequence_key " + OUTBOX_UPSERT_GUARD)
            # Rows imported before their contact existed get linked now, whatever their status.
            psql(db, f"UPDATE founderos_outbox SET contact_id = {link} WHERE id = {sql(oid)} AND contact_id IS NULL")
            imported += 1
    print(f"{imported} outbox entr{'y' if imported == 1 else 'ies'} imported or refreshed")
    if dry:
        return

    by_ident = {i.get("identifier"): i for i in issues}
    done = psql_rows(db, "SELECT id, workspace_id::text AS workspace_id, source_ref, status, attempt, to_address, subject, coalesce(error, '') AS error "
                         "FROM founderos_outbox WHERE source = 'paperclip' AND status IN ('sent', 'rejected', 'failed', 'uncertain') "
                         "AND (reported_attempt IS DISTINCT FROM attempt OR reported_status IS DISTINCT FROM status)")
    for r in done:
        issue = by_ident.get(r["source_ref"])
        if not issue:
            continue
        to, subject = r["to_address"], r["subject"]
        who = pod_senders.get(r["workspace_id"]) or sender
        line = {"sent": f"**Sent from TFG OS** ({who}) to {to}: \"{subject}\".",
                "simulated": f"**Dry run**: the email to {to} (\"{subject}\") went to the test inbox, not the prospect.",
                "rejected": (f"**Not sent** to {to}: \"{subject}\": {r['error']}. Do not resend it as-is." if r["error"] else
                             f"**Not sent: the owner rejected** the email to {to}: \"{subject}\". Do not resend it as-is."),
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
