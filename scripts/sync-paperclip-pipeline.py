#!/usr/bin/env python3
"""Sync Paperclip's researched prospects into the TFG OS sales pipeline.

Reads the prospects table from the Paperclip THE-3 "prospects" document, plus
a "prospects" document on any other task holding a JSON array (optionally in a
```json fence) of {"company", "website", "location", "buyer", "buyerRole",
"email", "score", "evidence", "nextAction"} (the daily research routine writes
these), and upserts one founderos_funnel_contacts row per account into the "Sales System
Build" (vantage) workspace at the first_touch stage, plus a dated "researched,
not yet contacted" touch. Recipient emails
are picked up from any later task document that lists an address next to the
company (e.g. THE-11). Safe to re-run: existing rows keep their pipeline
status, only research fields are refreshed.

Config: ~/.founderos/.env (PAPERCLIP_API_URL, PAPERCLIP_BOARD_KEY,
PAPERCLIP_COMPANY_ID) and desktop/backend-go/.env (DATABASE_URL).
Usage: python3 scripts/sync-paperclip-pipeline.py [--dry-run]
"""
import fcntl
import json
import os
import re
import subprocess
import sys
import urllib.request
from datetime import date
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
WORKSPACE_SLUG = "vantage"  # displayed as "Sales System Build"
PRODUCT = "Sales System Build"
VENTURES = ("vantage", "launchpad-cohort")  # founderos_funnel_contacts.venture CHECK


def venture_for(workspace, pods=None):
    """The funnel's venture for a target workspace: a client pod's contacts are
    'client'; other workspaces (TFG HQ, command workspaces) sell PRODUCT,
    which is Vantage's offer."""
    if pods and workspace in pods:
        return "client"
    return workspace if workspace in VENTURES else "vantage"


def product_for(workspace, pods=None):
    """What the contact is being sold: the pod's client, else TFG's PRODUCT."""
    return pods[workspace] if pods and workspace in pods else PRODUCT


POD_ROUTES_QUERY = ("SELECT coalesce(json_agg(json_build_object('project_id', a.project_id, 'slug', p.slug, "
                    "'client_name', p.client_name)), '[]') FROM founderos_pod_agents a "
                    "JOIN founderos_client_pods p ON p.id=a.pod_id WHERE a.project_id<>''")


def load_pods(run):
    """Pod bindings by board project. A failed lookup stops the import (Codex M1
    review #6): client research must never fall back into TFG's pipeline."""
    code, out, err = run()
    if code != 0:
        sys.exit("pod bindings unavailable, nothing imported: " + (err or "").strip()[:200])
    return {p["project_id"]: p for p in json.loads(out.strip() or "[]")}


def target_for(issue, pods, task_workspaces):
    """Where a task's research goes: its client pod (by board project, docs/tfg/CLIENT-PODS.md),
    else its durable command's workspace, else the default pipeline. Never model text."""
    pod = pods.get(issue.get("projectId") or "")
    if pod:
        return pod["slug"]
    return task_workspaces.get(issue["id"], WORKSPACE_SLUG)


def scan_for_hq_addresses(issue, pods):
    """Only HQ tasks' free-text documents may supply addresses for HQ accounts."""
    return (issue.get("projectId") or "") not in pods
EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")
LINK = re.compile(r"\[([^\]]+)\]\(([^)]+)\)")
FENCE = re.compile(r"```(?:json)?\s*(\[.*?\])\s*```", re.S)
OWN_DOMAIN = "@thefoundersgroup.net"


def read_env(path):
    env = {}
    for line in Path(path).expanduser().read_text().splitlines():
        if "=" in line and not line.lstrip().startswith("#"):
            k, v = line.split("=", 1)
            env[k.strip()] = v.strip().strip('"')
    return env


def paperclip(env, path):
    req = urllib.request.Request(env["PAPERCLIP_API_URL"].rstrip("/") + path,
                                 headers={"Authorization": "Bearer " + env["PAPERCLIP_BOARD_KEY"]})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read().decode(), strict=False)


def doc_body(doc):
    return doc.get("body") or (doc.get("latestRevision") or {}).get("body") or ""


def table_rows(body):
    for line in body.splitlines():
        if line.startswith("|") and not re.match(r"^\|\s*-", line):
            yield [c.strip() for c in line.strip().strip("|").split("|")]


def json_prospects(body):
    """Accounts from a JSON prospects document, and how many entries were skipped."""
    m = FENCE.search(body)
    try:
        data = json.loads(m.group(1) if m else body.strip())
    except json.JSONDecodeError as e:
        raise ValueError(f"prospects document is not a JSON array: {e}") from None
    if not isinstance(data, list):
        raise ValueError("prospects document must be a JSON array")
    out, bad = [], 0
    for e in data:
        name = str(e.get("company") or "").strip() if isinstance(e, dict) else ""
        if not name:
            bad += 1
            continue
        email = str(e.get("email") or "").strip()
        if not EMAIL.fullmatch(email) or email.lower().endswith(OWN_DOMAIN):
            email = None
        buyer = str(e.get("buyer") or "").strip() or None
        if buyer and re.search(r"unknown|verify", buyer, re.I):
            buyer = None
        score = e.get("score")
        if not (isinstance(score, int) and not isinstance(score, bool) and 0 <= score <= 100):
            score = None
        out.append({"name": name, "url": str(e.get("website") or "").strip() or None,
                    "location": str(e.get("location") or ""), "evidence": str(e.get("evidence") or ""),
                    "buyer": buyer, "buyerRole": str(e.get("buyerRole") or "").strip() or None, "score": score, "next": str(e.get("nextAction") or ""), "email": email})
    return out, bad


def address_for_company(name, text, addr):
    """Whether addr, found in a table row's text, is this company's: the name appears
    as a whole word and the address's domain belongs to it ("Rise" is not Sandstone's)."""
    if not re.search(r"(?<![a-z0-9])" + re.escape(name.lower()) + r"(?![a-z0-9])", text.lower()):
        return False
    domain = addr.rsplit("@", 1)[1].lower()
    stem = domain.split(".")[0]
    compact_name = re.sub(r"[^a-z0-9]", "", name.lower())
    compact_domain = re.sub(r"[^a-z0-9]", "", domain)
    first = re.sub(r"[^a-z0-9]", "", name.lower().split()[0]) if name.split() else ""
    return ((len(stem) >= 3 and stem in compact_name) or (len(compact_name) >= 3 and compact_name in compact_domain)
            or (len(first) >= 4 and first in stem))


def slug(s):
    return re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")


def sql(v):
    if v is None:
        return "NULL"
    if isinstance(v, (int, float)):
        return str(v)
    return "'" + str(v).replace("'", "''") + "'"


def prospect_id(workspace, company):
    # Retain historical IDs, and isolate new command/client records.
    return "fc-pc-" + (slug(workspace) + "-" if workspace != WORKSPACE_SLUG else "") + slug(company)


def main():
    dry = "--dry-run" in sys.argv
    pc = read_env("~/.founderos/.env")
    db = read_env(REPO / "desktop/backend-go/.env")["DATABASE_URL"]
    company = pc["PAPERCLIP_COMPANY_ID"]
    state = Path.home() / ".founderos/state"
    state.mkdir(parents=True, exist_ok=True)
    lock = open(state / "paperclip-pipeline.lock", "w")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        print("another pipeline sync is running; skipping")
        return
    # A task inherits its target from a durable OS command, not from model text.
    mapping_sql = "SELECT COALESCE(jsonb_object_agg(step->>'taskId',w.slug),'{}'::jsonb) FROM founderos_commands c JOIN workspaces w ON w.id=c.workspace_id CROSS JOIN LATERAL jsonb_array_elements(c.steps) step WHERE step->>'taskId' IS NOT NULL"
    mapped = subprocess.run(["psql", db, "-At", "-c", mapping_sql], capture_output=True, text=True)
    task_workspaces = json.loads(mapped.stdout.strip()) if mapped.returncode == 0 and mapped.stdout.strip() else {}
    def run_pod_query():
        r = subprocess.run(["psql", db, "-At", "-c", POD_ROUTES_QUERY], capture_output=True, text=True)
        return r.returncode, r.stdout, r.stderr
    pods = load_pods(run_pod_query)
    pod_products = {p["slug"]: p["client_name"] for p in pods.values()}


    issues = paperclip(pc, f"/api/companies/{company}/issues")
    issues = issues.get("issues", issues) if isinstance(issues, dict) else issues
    issues = sorted(issues, key=lambda i: (i.get("updatedAt") or i.get("createdAt") or "", i.get("id", "")))
    by_ident = {i.get("identifier"): i for i in issues}

    prospects = paperclip(pc, f"/api/issues/{by_ident['THE-3']['id']}/documents/prospects")
    accounts = []
    for row in table_rows(doc_body(prospects)):
        if len(row) < 7 or row[0].lower().startswith("company"):
            continue
        m = LINK.search(row[0])
        name, url = (m.group(1), m.group(2)) if m else (row[0], None)
        buyer = re.sub(r"\s*\([^)]*\)", "", row[4]).strip()  # "Joe Lambert (director)" -> "Joe Lambert"
        if not buyer or re.search(r"unknown|verify|;|/", buyer, re.I):
            buyer = None
        score = int(row[5]) if row[5].isdigit() else None
        accounts.append({"name": name, "url": url, "location": row[2], "evidence": row[3],
                         "buyer": buyer, "score": score, "next": row[6]})

    # Later tasks: JSON "prospects" documents add accounts, and any table row
    # naming a known company with an email in it gives its recipient address.
    emails = {}
    other_docs = []
    for ident, issue in by_ident.items():
        if ident in (None, "THE-3"):
            continue
        docs = paperclip(pc, f"/api/issues/{issue['id']}/documents")
        docs = docs.get("documents", docs) if isinstance(docs, dict) else docs
        for d in docs:
            if d.get("key") != "prospects":
                if scan_for_hq_addresses(issue, pods):
                    other_docs.append(d)
                continue
            try:
                found, bad = json_prospects(doc_body(d))
            except ValueError as e:
                print(f"skipped {ident} prospects: {e}", file=sys.stderr)
                continue
            if bad:
                print(f"{ident} prospects: skipped {bad} entries without a company", file=sys.stderr)
            target = target_for(issue, pods, task_workspaces)
            known = {(a.get("workspace", WORKSPACE_SLUG), slug(a["name"])) for a in accounts}
            for a in found:
                a["workspace"] = target
                a["sourceRef"] = ident
                if a["email"]:
                    emails[(target, a["name"])] = a["email"]
                if (target, slug(a["name"])) not in known:
                    accounts.append(a)
                    known.add((target, slug(a["name"])))
                else:
                    existing = next(item for item in accounts if (item.get("workspace", WORKSPACE_SLUG), slug(item["name"])) == (target, slug(a["name"])))
                    existing.update({key: value for key, value in a.items() if value not in (None, "")})
    for d in other_docs:
        for row in table_rows(doc_body(d)):
            text = " ".join(row)
            found = [e for e in EMAIL.findall(text) if not e.lower().endswith(OWN_DOMAIN)]
            if not found:
                continue
            for a in accounts:
                if a.get("workspace", WORKSPACE_SLUG) != WORKSPACE_SLUG:
                    continue
                match = next((e for e in found if address_for_company(a["name"], text, e)), None)
                if match:
                    emails.setdefault((WORKSPACE_SLUG, a["name"]), match)

    ws = subprocess.run(["psql", db, "-At", "-c", f"select id from workspaces where slug={sql(WORKSPACE_SLUG)}"],
                        capture_output=True, text=True, check=True).stdout.strip()
    if not ws:
        sys.exit(f"workspace {WORKSPACE_SLUG!r} not found")

    workspace_ids = {WORKSPACE_SLUG: ws}
    for target in {a.get("workspace", WORKSPACE_SLUG) for a in accounts}:
        if target not in workspace_ids:
            result = subprocess.run(["psql", db, "-At", "-c", f"select id from workspaces where slug={sql(target)}"], capture_output=True, text=True, check=True)
            if not result.stdout.strip():
                sys.exit("command workspace no longer exists")
            workspace_ids[target] = result.stdout.strip()
    today = date.today().isoformat()
    stmts = ["BEGIN;"]
    for a in accounts:
        target = a.get("workspace", WORKSPACE_SLUG)
        ws = workspace_ids[target]
        cid = prospect_id(target, a["name"])
        email = emails.get((target, a["name"]))
        stmts.append(
            "INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, product, relationship, "
            "likelihood, email, person, company, website, location, buyer_role, research_evidence, next_action, research_source_ref, created_at) VALUES ("
            f"{sql(cid)}, {sql(ws)}, {sql(a['buyer'] or a['name'])}, {sql(venture_for(target, pod_products))}, 'first_touch', {sql(product_for(target, pod_products))}, 'cold', "
            f"{sql(a['score'])}, {sql(email)}, {sql(a['buyer'])}, {sql(a['name'])}, {sql(a.get('url'))}, {sql(a.get('location'))}, {sql(a.get('buyerRole'))}, {sql(a.get('evidence'))}, {sql(a.get('next'))}, {sql(a.get('sourceRef', 'THE-3'))}, now()) "
            "ON CONFLICT (id) DO UPDATE SET likelihood = EXCLUDED.likelihood, person = EXCLUDED.person, "
            "company = EXCLUDED.company, email = COALESCE(EXCLUDED.email, founderos_funnel_contacts.email), "
            "website = COALESCE(NULLIF(EXCLUDED.website,''),founderos_funnel_contacts.website), "
            "location = COALESCE(NULLIF(EXCLUDED.location,''),founderos_funnel_contacts.location), "
            "buyer_role = COALESCE(NULLIF(EXCLUDED.buyer_role,''),founderos_funnel_contacts.buyer_role), "
            "research_evidence = COALESCE(NULLIF(EXCLUDED.research_evidence,''),founderos_funnel_contacts.research_evidence), "
            "next_action = COALESCE(NULLIF(EXCLUDED.next_action,''),founderos_funnel_contacts.next_action), "
            "research_source_ref = EXCLUDED.research_source_ref;")
        stmts.append(
            "INSERT INTO founderos_funnel_touches (id, workspace_id, contact_id, seq, stage, channel, label, source, at) VALUES ("
            f"{sql(cid + '-research')}, {sql(ws)}, {sql(cid)}, 1, 'first_touch', 'crm', "
            f"{sql('Researched by Paperclip (not yet contacted): ' + a['evidence'][:180])}, 'manual', {sql(today)}) ON CONFLICT (id) DO NOTHING;")
    stmts.append("COMMIT;")

    print(f"{len(accounts)} accounts, {len(emails)} with a recipient address")
    if dry:
        for a in accounts:
            print(f"  {a['score'] or '-':>3}  {a['name']:<28} {a['buyer'] or '(buyer unknown)':<30} {emails.get((a.get('workspace', WORKSPACE_SLUG), a['name']), '')}")
        return
    r = subprocess.run(["psql", db, "-q", "-v", "ON_ERROR_STOP=1"], input="\n".join(stmts), text=True, capture_output=True)
    if r.returncode:
        sys.exit("sync failed, nothing written: " + r.stderr.strip().splitlines()[0])
    print("synced into TFG OS pipelines: " + ", ".join(sorted(workspace_ids)))


if __name__ == "__main__":
    main()
