# mailreceipt

Turn a dropped email plus a Postfix mail log into a cited, verifiable delivery
receipt — per recipient: delivered, bounced, deferred, or not found, each quoting
the verbatim log line as evidence.

## Install

```
go install github.com/ppiankov/mailreceipt/cmd/mailreceipt@latest
```

## Commands

### mailreceipt check

Reads a dropped email (RFC822 file or a pasted top-of-thread block, or stdin) and
a Postfix mail log, then reports a cited delivery outcome per recipient. Correlates
by Message-ID when present, else by recipient + time window. Never invents an
outcome: a recipient with no matching log line is reported `not_found`.

**Flags:**
- `--log path` — path to the Postfix mail log (required unless set in .mailreceipt.yml)
- `--format json` — output as JSON (default `md` for human-readable Markdown)
- `--case string` — case/matter reference stamped on the receipt
- `--log-year int` — year for year-less BSD syslog timestamps (default 2026; RFC3339 logs self-date)

**JSON output:**
```json
{
  "artifact_type": "mail_delivery_receipt",
  "tool": "mailreceipt",
  "case": "CASE-001",
  "summary": "mixed",
  "summary_counts": {"delivered": 4, "not_found": 1},
  "result": {
    "message_id": "09f201dcf508@acme.test",
    "subject": "RE: reminder",
    "recipients": [
      {
        "recipient": "docketing@client.test",
        "outcome": "delivered",
        "match_method": "message_id",
        "relay": "mx.example.test[203.0.113.25]:25",
        "response": "250 2.6.0 Queued mail for delivery",
        "time": "2026-06-05T18:26:48+02:00",
        "citation": "2026-06-05T18:26:48.562640+02:00 mail postfix/smtp[3697441]: 304A51600511: to=<docketing@client.test>, relay=..., status=sent (250 2.6.0 Queued mail for delivery)"
      }
    ],
    "caveat": "A 'delivered' outcome means the remote mail server accepted the message (SMTP 2xx) at relay handoff. It does not prove a person read it. This receipt reports transport, not attention."
  }
}
```

`summary` is one of `delivered`, `bounced`, `deferred`, `not_found`, or `mixed`
(more than one outcome across recipients); `summary_counts` is the per-outcome
tally the summary is derived from. Every delivered/bounced/deferred recipient
carries a `citation` — the verbatim log line. The `outcome` and `response` are
the mail server's own disposition, quoted, never a model's interpretation.

**Exit codes:**
- 0: analysis succeeded (any outcome, including bounced or not_found, is exit 0)
- 1: could not analyze (email unparseable, no recipients, log unreadable, bad --format)

### mailreceipt verify

Re-checks a JSON receipt against a fresh read of the log: every cited line must
still appear verbatim. This is the auditor's command — it proves a receipt was not
fabricated or edited after the fact.

**Flags:**
- `--log path` — path to the Postfix mail log to verify against (required unless set in .mailreceipt.yml)

**Exit codes:**
- 0: every citation in the receipt is present verbatim in the log
- 1: one or more citations missing (tampering or wrong log), or the receipt/log could not be read

### mailreceipt doctor

Diagnoses a mail log before a `not_found` result is trusted, so "could not read
the evidence" never looks like "the evidence says not delivered." Checks that the
log exists, is readable and non-empty, what timestamp format it uses, and how many
Postfix delivery lines it holds.

**Flags:**
- `--log path` — path to the Postfix mail log to diagnose (required)
- `--format json` — output as JSON (default `md`)

**JSON output:**
```json
{
  "tool": "mailreceipt",
  "version": "0.3.0",
  "source": {"repo": "https://github.com/ppiankov/mailreceipt"},
  "status": "ok",
  "checks": [
    {"name": "log_exists", "status": "pass", "detail": "/var/log/mail.log"},
    {"name": "log_readable", "status": "pass", "detail": "readable"},
    {"name": "log_nonempty", "status": "pass", "detail": "2076 bytes"},
    {"name": "delivery_lines", "status": "pass", "detail": "142 delivery event(s)"},
    {"name": "timestamp_format", "status": "pass", "detail": "RFC3339 (self-dating; --log-year ignored)"}
  ]
}
```

`status` is the worst of the per-check statuses (`pass`, `warn`, `fail`).

**Exit codes:**
- 0: all checks pass or warn (log is usable)
- 1: a check failed (log missing, unreadable, or empty)

### mailreceipt init

Writes a `.mailreceipt.yml` in the current directory holding default values for
`log`, `log_year`, and a `case_prefix`. `check` and `verify` read it as defaults;
an explicit flag always overrides the config. Useful when running mailreceipt
repeatedly against one server's log.

**Flags:**
- `--force` — overwrite an existing `.mailreceipt.yml`

**Exit codes:**
- 0: config written
- 1: file already exists (without --force) or could not be written

## Handoffs

- Output: `mail_delivery_receipt` JSON. Next: attach to a case/matter system, or
  feed to `mailreceipt verify` later to prove the receipt is unaltered.
- Output: `doctor` JSON (`status` + `checks`). Next: an orchestrator decides
  whether a `not_found` from `check` is trustworthy (log usable) or noise (log
  empty/wrong format).
- Refused questions: whether a human read the email, whether delivery is legally
  sufficient, what to do about a bounce. mailreceipt reports transport only.

## What this does NOT do

- Does not prove a human read the email — `delivered` means the remote server
  accepted the message at relay handoff (SMTP 2xx), transport not attention.
- Does not judge legal sufficiency — it states what the log shows; the case call
  is the operator's.
- Does not send, retry, or modify mail — it reads logs the server already wrote.
- Does not track opens or clicks — no pixels, no beacons, no read receipts.
- Does not invent outcomes — a recipient with no matching log line is `not_found`,
  never silently upgraded to delivered.

## Failure Modes

- Empty or unreadable log: `check` would report every recipient `not_found`. Run
  `mailreceipt doctor --log <path>` first — it returns `fail` and names the cause.
  Distrust any `not_found` until doctor reports the log readable with delivery lines.
- Wrong file / not Postfix syslog: `doctor` reports 0 delivery lines (`warn`).
  Distrust `not_found`; the receipt is measuring the wrong evidence.
- Year-less BSD timestamps without `--log-year`: delivery times stamp with the
  default year and may be wrong for old logs. RFC3339 logs self-date and ignore it.

## Parsing examples

```bash
# Overall verdict and the per-outcome tally behind it:
mailreceipt check mail.eml --log /var/log/mail.log --format json | jq '.summary, .summary_counts'

# Just the delivered recipients, with their cited evidence:
mailreceipt check mail.eml --log /var/log/mail.log --format json \
  | jq '.result.recipients[] | select(.outcome == "delivered") | {recipient, citation}'

# Is the log even usable before trusting a not_found?
mailreceipt doctor --log /var/log/mail.log --format json | jq '.status'
```

---

This tool follows the [Agent-Native CLI Convention](https://ancc.dev). Validate with: `ancc validate .`
