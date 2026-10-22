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
                    "buyer": buyer, "score": score, "next": str(e.get("nextAction") or ""), "email": email})
    return out, bad


def slug(s):
    return re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")


def sql(v):
    if v is None:
        return "NULL"
    if isinstance(v, (int, float)):
        return str(v)
    return "'" + str(v).replace("'", "''") + "'"


def main():
    dry = "--dry-run" in sys.argv
    pc = read_env("~/.founderos/.env")
    db = read_env(REPO / "desktop/backend-go/.env")["DATABASE_URL"]
    company = pc["PAPERCLIP_COMPANY_ID"]

    issues = paperclip(pc, f"/api/companies/{company}/issues")
    issues = issues.get("issues", issues) if isinstance(issues, dict) else issues
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
                other_docs.append(d)
                continue
            try:
                found, bad = json_prospects(doc_body(d))
            except ValueError as e:
                print(f"skipped {ident} prospects: {e}", file=sys.stderr)
                continue
            if bad:
                print(f"{ident} prospects: skipped {bad} entries without a company", file=sys.stderr)
            known = {slug(a["name"]) for a in accounts}
            for a in found:
                if a["email"]:
                    emails.setdefault(a["name"], a["email"])
                if slug(a["name"]) not in known:
                    accounts.append(a)
                    known.add(slug(a["name"]))
    for d in other_docs:
        for row in table_rows(doc_body(d)):
            text = " ".join(row)
            found = [e for e in EMAIL.findall(text) if not e.lower().endswith(OWN_DOMAIN)]
            if not found:
                continue
            for a in accounts:
                if a["name"].lower() in text.lower():
                    emails.setdefault(a["name"], found[0])

    ws = subprocess.run(["psql", db, "-At", "-c", f"select id from workspaces where slug={sql(WORKSPACE_SLUG)}"],
                        capture_output=True, text=True, check=True).stdout.strip()
    if not ws:
        sys.exit(f"workspace {WORKSPACE_SLUG!r} not found")

    today = date.today().isoformat()
    stmts = ["BEGIN;"]
    for a in accounts:
        cid = "fc-pc-" + slug(a["name"])
        email = emails.get(a["name"])
        stmts.append(
            "INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, product, relationship, "
            "likelihood, email, person, company, created_at) VALUES ("
            f"{sql(cid)}, {sql(ws)}, {sql(a['buyer'] or a['name'])}, {sql(WORKSPACE_SLUG)}, 'first_touch', {sql(PRODUCT)}, 'cold', "
            f"{sql(a['score'])}, {sql(email)}, {sql(a['buyer'])}, {sql(a['name'])}, now()) "
            "ON CONFLICT (id) DO UPDATE SET likelihood = EXCLUDED.likelihood, person = EXCLUDED.person, "
            "company = EXCLUDED.company, email = COALESCE(EXCLUDED.email, founderos_funnel_contacts.email);")
        stmts.append(
            "INSERT INTO founderos_funnel_touches (id, workspace_id, contact_id, seq, stage, channel, label, source, at) VALUES ("
            f"{sql(cid + '-research')}, {sql(ws)}, {sql(cid)}, 1, 'first_touch', 'crm', "
            f"{sql('Researched by Paperclip (not yet contacted): ' + a['evidence'][:180])}, 'manual', {sql(today)}) ON CONFLICT (id) DO NOTHING;")
    stmts.append("COMMIT;")

    print(f"{len(accounts)} accounts, {len(emails)} with a recipient address")
    if dry:
        for a in accounts:
            print(f"  {a['score'] or '-':>3}  {a['name']:<28} {a['buyer'] or '(buyer unknown)':<30} {emails.get(a['name'], '')}")
        return
    r = subprocess.run(["psql", db, "-q", "-v", "ON_ERROR_STOP=1"], input="\n".join(stmts), text=True, capture_output=True)
    if r.returncode:
        sys.exit("sync failed, nothing written: " + r.stderr.strip().splitlines()[0])
    print("synced into TFG OS pipeline (Sales System Build)")


if __name__ == "__main__":
    main()
