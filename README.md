# mailreceipt

> Attach proof, not vibes.

![status](https://img.shields.io/badge/status-MVP-orange)
![language](https://img.shields.io/badge/go-1.26-00ADD8)
[![ANCC](https://img.shields.io/badge/ANCC-compliant-brightgreen)](https://ancc.dev)

A client says they never got the email. Your log says otherwise. `mailreceipt` turns
that log into a **cited delivery receipt** you can attach to the case.

```sh
mailreceipt check reminder.eml --log /var/log/mail.log --case CASE-001
```

```
# Mail Delivery Receipt

**Case:** CASE-001
**Overall:** Mixed — 1 delivered (remote), 1 delivered (local), 1 bounced

| Recipient                | Outcome              | When             | Evidence |
|--------------------------|----------------------|------------------|----------|
| jdoe@exampleclient.test  | ✅ delivered         | 2026-06-05 15:41 | 250 2.0.0 OK |
| filing@exampleclient.test| 📥 delivered (local) | 2026-06-05 15:41 | delivered via mailreceipt service |
| team@exampleclient.test  | ⛔ bounced           | 2026-06-05 15:09 | 550 5.1.1 User unknown |

## Evidence (verbatim log lines)
- jdoe@…:    Jun 5 15:41:55 … postfix/smtp … relay=mx.client[203.0.113.25]:25 … status=sent (250 2.0.0 OK)
- filing@…:  Jun 5 15:41:55 … postfix/pipe … relay=mailreceipt … status=sent (delivered via mailreceipt service)
- team@…:    Jun 5 15:09:21 … status=bounced (… 550 5.1.1 … User unknown …)

> This receipt covers two handoff types. A 'delivered' (remote) outcome means a
> remote mail server accepted the message at relay handoff; a 'delivered local'
> outcome means this mail server handed the message to a local transport, with no
> remote relay acceptance observed. This receipt reports transport, not attention.
```

Every outcome is the disposition your mail server recorded, quoted verbatim.
No model in the delivery path — a 250 is a 250, a 550 is a 550.

## What the receipt proves

- **Delivered (remote)** — a remote mail server accepted the message (SMTP 2xx) at relay handoff
- **Delivered (local)** — a local transport (pipe/mailbox/service) accepted it; *not* relayed to a remote server, *not* an SMTP 2xx. The receipt says so, and never claims otherwise
- **Bounced** — permanent rejection (5xx); the message will not be retried
- **Deferred** — temporary failure (4xx); the message is in queue
- **Not found** — no matching log entry; the tool will not invent an outcome

Each outcome cites the verbatim log line it came from. The receipt is
human-readable Markdown or machine-readable JSON (`--format json`) — both are
meant to be attached to a matter, ticket, or claim.

## What it does not prove

- **Not proof a human read it.** Transport acceptance ≠ reading. Every receipt
  says so explicitly.
- **Not legal sufficiency.** The tool states what the log shows. What that means
  for your case is the operator's call.
- **Not mailbox state.** It does not know whether a delivered message was later
  filtered, discarded, or read.
- **Not advice.** A bounce is a fact. What to do about it is yours to decide.

## What it is NOT

- Not a mail client or an MTA — it reads logs your server already wrote
- Not a summarizer — it answers one question per recipient with evidence
- Not an AI verdict — deterministic log parsing, no model in the delivery path
- Not a tracker — no pixels, no beacons, no read receipts

## Why ANCC

`mailreceipt` is a demonstration of the ANCC principle applied to email evidence:

**Raw evidence → deterministic receipt → cited per-recipient outcome → attachable artifact → verifiable against source.**

The tool is a mirror, not an oracle. It finds the right log line, bounds its meaning
honestly, and refuses to assert anything it cannot cite. This is what makes the
output safe to attach to a case and safe to act on.

## Agent-safe use

`mailreceipt` is designed to work with or without an agent in the loop. When an
agent drives the workflow, run it behind a file-access gate such as
[Bulwark](https://obstalabs.dev/bulwark) so the agent reads only the
specific evidence needed — not the whole mailbox.

```sh
# The agent gets the minimum evidence. The output is cited by construction.
bulwark run --protect ./mail-evidence -- \
  mailreceipt check reminder.eml --log mail.log --case CASE-123
```

This is the composition:

```
Bulwark        limits what the agent may read
mailreceipt    defines the cited artifact
agent          operates the workflow, does not invent facts
operator       decides what to attach, send, or escalate
```

No mailbox-wide rummaging. No uncited summary. No over-permissioned agent.

## Build and run

```sh
go build -o mailreceipt ./cmd/mailreceipt

# Check a dropped email against your mail log:
mailreceipt check testdata/reminder-1509.eml --log testdata/mail.log --case DEMO-1

# As an attachable JSON artifact:
mailreceipt check testdata/reminder-1509.eml --log testdata/mail.log --format json > DEMO-1.receipt.json

# Audit a receipt later — every citation must still appear in the log:
mailreceipt verify DEMO-1.receipt.json --log testdata/mail.log
```

The email may be a file argument or piped on stdin. It accepts a real RFC822
message or a pasted top-of-thread block (the messy `From:/Sent:/To:` forwarded
format). When there is no `Message-ID`, it falls back to recipient + time-window
matching.

## Receipt-by-email filter

`mailreceipt filter` is for an internal alias such as `receipt@yourdomain.test`.
It reads the trigger email on stdin and writes a reply email only when the
authenticated envelope sender is allowed by `.mailreceipt.yml`.

### Security model

Filter authorization is only as strong as the MTA-authenticated envelope sender
passed to `--envelope-from`. The Postfix alias MUST pass the authenticated SMTP
envelope sender, not the forgeable message `From:` header. The receipt alias MUST
NOT be reachable by unauthenticated external inbound mail; expose it only behind
the internal mail path that supplies the authenticated envelope identity.

If this trust boundary is miswired, an attacker can spoof an allowed sender and
cause the bot to disclose delivery records in an automatic reply. The filter
fails closed for empty, malformed, unauthorized, looped, or unreadable requests,
but it cannot authenticate SMTP by itself.

```yaml
receipt_filter:
  domains: [acme.test]
  reply_from: receipt@acme.test
  teams:
    docketing:
      members: [docketing@acme.test, assistant1@acme.test, attorney1@acme.test]
```

Wire the alias so the MTA passes its authenticated envelope sender, not the
forgeable `From:` header:

```sh
mailreceipt filter --envelope-from "$SENDER" --from receipt@acme.test --log /var/log/mail.log
```

Forward the original sent message as an `.eml` / `message/rfc822` attachment.
Inline forwards are tolerated for pasted-header workflows, but attachments
preserve the Message-ID and keep correlation exact. Base64 or quoted-printable
encoded `.eml` attachments are supported when the attachment is explicitly a
sent message (`message/rfc822` or a `.eml` filename). Unauthorized senders, team
mismatches, loops (`Auto-Submitted` or `Precedence: bulk`), malformed requests,
and unreadable forwarded attachments produce no reply.

## Failure modes

- Pasted top-of-thread blocks with no `Message-ID` use recipient + parsed send
  time. If the pasted `Sent:`/`Date:` value is unparseable, mailreceipt returns
  `not_found` rather than matching every log event for that recipient.

## JSON output

```sh
mailreceipt check reminder.eml --log mail.log --format json
```

Returns a structured artifact with `artifact_type`, `summary`, `summary_counts`
(the per-outcome tally behind the summary), and per-recipient `outcome` /
`match_method` / `citation` / `relay` / `response` / `time`. When recipients
resolve to more than one outcome the summary is `mixed` and the headline states
the split. Attach it to a case, feed it to a downstream system, or store it as
evidence.

## Verifying a receipt

```sh
mailreceipt verify CASE-123.receipt.json --log mail.log
```

Re-checks that every cited log line still appears verbatim in the log file.
An edited or fabricated citation fails. A receipt that passes `verify` is
evidence that the artifact has not been altered since it was created.

## How it works

```
email.eml ──► extract Message-ID + recipients        [internal/eml]
mail.log  ──► parse to per-recipient delivery events  [internal/maillog]
              │
              ├─ correlate: message-id match, else recipient+window  [internal/deliver]
              ├─ pick authoritative event (latest; terminal > deferred)
              └─ outcome per recipient, each citing its raw log line
                     │
                     └─► receipt (md / json), every fact cited  [internal/receipt]
```

## Limitations

- **Postfix syslog format** only. Exim, Sendmail, journald/JSON not yet supported.
  Both timestamp styles are read: traditional BSD (`Jun  5 14:09:36`) and
  RFC3339 / ISO-8601 (`2026-06-05T14:09:36+02:00`, the modern rsyslog default).
- **Self-hosted / relayed mail only.** If your mail goes through Gmail or
  Microsoft 365 with no local log, there is nothing to read.
- **Year-less BSD timestamps** default to the current year; set `--log-year`
  for older logs. RFC3339 timestamps are self-dating and ignore `--log-year`.
- **Transport evidence only.** The receipt cannot see past relay handoff.

## License

mailreceipt is licensed under the GNU Affero General Public License v3.0.
See [LICENSE](LICENSE).

Copyright (c) 2026 ppiankov
