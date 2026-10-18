# Client install

Every client gets their own copy of TFG OS on their own branch, their own brand
file, and a clean database. TFG's improvements reach them by merging the `tfg`
branch into theirs.

## 1. Create the client branch (Karim, 2 minutes)

```bash
git switch tfg && git pull
scripts/tfg-new-client.sh acme "Acme Logistics" "Sarah" acme.com
```

This creates branch `client/acme` and `config/tfg/clients/acme.yaml`, generates
the brand files and commits. It fails before touching git if anything is wrong.

## 2. Fill in the brand file with the client (kickoff call)

Open `config/tfg/clients/acme.yaml`. Every line marked `CLIENT:` is theirs to decide:

- **The two pipelines**: names, colours, and the `lead_match` words that send a
  lead to the first one.
- **`minutes_per_successful_run`**: how many minutes of a rep's time one
  successful agent run replaces. **Agree this number with the client in
  writing.** It drives the "hours handed back" figure they will judge you on.
- **`hide_views`**: views they do not need.

Then:

```bash
make tfg-brand BRAND=config/tfg/clients/acme.yaml
git commit -am "Acme: brand agreed at kickoff"
```

## 3. Run it (days 1-3 of the 10-day build)

Where it runs is your call per client: their own Mac, or a server you host for
them (see MAINTENANCE.md, "Hosting").

```bash
BUSINESSOS_HEADLESS=1 make dev-local
# the client's operator registers at /register
make client OWNER=<their email>
make dev-local-restart
```

`make client` loads TFG's agents, rules and SOP templates but **none of TFG's
own people, SOPs or roadmap**, and no demo records.

## 4. Connect their accounts (days 1-3)

On the Connections view (or in `~/.founderos/.env` on that machine), add their:
inbox, calendar, intake form (Typeform), call recorder (Fathom), payments
(Stripe), Slack. Each connector shows `connected`, `not configured` or `error`.
Nothing shows green unless it is.

The client enters their own passwords and keys. TFG never needs to see them.

## 5. AI SDRs on, then simulate (days 4-10)

1. Run each agent by hand from its card on the Org Chart view and check the output with the
   client: Pipeline SDR, CRM Hygiene, Impact Reporter.
2. Push one test lead from form to signed proposal and watch every view update.
3. Fix gaps, write them up in the Brain, delete the test lead.
4. Only then: `FOUNDEROS_CRONS=1` and switch on outreach.

## 6. Every week after

- Friday: the Impact Reporter's summary goes in the client update, with its formula.
- When `tfg` gets a fix: `git switch client/acme && git merge tfg`, run
  `make tfg-check BRAND=config/tfg/clients/acme.yaml`, then restart their stack.
