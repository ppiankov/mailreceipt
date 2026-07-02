# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed
- Receipts now resolve for messages forwarded as an attachment from Outlook.
  Outlook's "forward as attachment" strips the original message's `Message-ID`
  header (keeping only its `Date`), leaving no exact key to correlate on, so every
  recipient reported `not_found`. When a forwarded message has no `Message-ID` but
  has a `Date`, delivery is now correlated by recipient and send-time window —
  but only when the match is unambiguous (a single message, i.e. one Postfix
  queue-id). If the window contains more than one message to the same recipient,
  the result stays `not_found` rather than guess which send was meant. Messages
  that do carry a `Message-ID` are unaffected and still use exact-ID correlation.

## [0.6.1] - 2026-07-02

### Fixed
- Dovecot Sieve mailbox deliveries are now recognized. When Sieve performs the
  final store (common for foldering, vacation, and default keep), Dovecot logs
  `stored mail into mailbox '<name>'` instead of `saved mail to <name>`. Only the
  latter was recognized, so Sieve-delivered internal mail — the majority of local
  deliveries on a typical Postfix + Dovecot server — was reported `not_found`.
  Both markers are now treated as `delivered_local`; Sieve `forwarded` and
  `discarded` outcomes are correctly not counted as local delivery.
- Recipients are recovered from Outlook-mangled address headers. Forwarded Outlook
  messages wrap `To:`/`Cc:` with quoted-printable soft breaks (including
  mid-address, with no following whitespace), a doubled `< <mailto:a@x> a@x>` form,
  and `;` separators. These previously parsed to zero recipients, so the filter
  produced no receipt. The headers are now unfolded and normalized before address
  parsing. RFC2047 encoded-word subjects (which also end a line in `=`) are left
  intact.
- The receipt filter no longer refuses silently. Every terminal state — no
  attachment, zero parsed recipients, unauthorized sender, loop/dedup suppression,
  unreadable log — now writes a specific reason to stderr while leaving stdout
  empty (so the Postfix wrapper still sends no reply). A request that could not
  produce a receipt is now diagnosable instead of invisible.
- The `member:` (singular) key in a `receipt_filter` team is now accepted as an
  alias of `members:`. The singular form previously emptied the team silently, so
  a sender listed only under `member:` was refused a receipt with no diagnostic.
- Alias deliveries now correlate to the right recipient. When `/etc/aliases`
  redirects an address to another mailbox (e.g. `j.smith: docketing`),
  Postfix delivers to the target mailbox but logs `orig_to=<original-address>`
  alongside `to=<alias-target>`. mailreceipt now reads `orig_to` and matches a
  recipient against it, so an aliased delivery resolves to `delivered_local` for the
  address the message was actually sent to — using the bridge Postfix logs, with no
  `/etc/aliases` parsing. Covers `postfix/local` → maildrop mailbox deliveries.

### Added
- Dovecot LDA/LMTP local-delivery lines are now recognized. On the common
  Postfix + Dovecot setup, Postfix hands internal mail to `dovecot-lda`, which logs
  the mailbox save under the `dovecot` tag rather than as a `postfix/local` line —
  so internal deliveries previously showed as `not_found`. A Dovecot `saved mail to`
  line is now reported as `delivered_local` (a local mailbox handoff, not a
  remote-server acceptance). Dovecot logs the final **mailbox name** after
  `/etc/aliases` remapping (often a bare username unrelated to the address), so the
  correlation joins on the **Message-ID**, which survives every alias hop: for a
  single-recipient message the delivery is attributed even when the mailbox name
  differs from the address. With multiple recipients and an alias-remapped mailbox
  that matches none of them, the delivery is left `not_found` rather than guessed.
  Dovecot error/discard lines are not treated as delivery.
- `filter --log` accepts a single path, a comma-separated list, or a glob, and
  reads `.gz` rotated logs. A receipt request for a message older than the current
  log no longer misses the delivery in a rotated file, and a `not_found` result
  reports the time range actually searched.

## [0.5.1] - 2026-06-09

### Added
- `filter --dedup-dir` (and `receipt_filter.dedup_dir` config) — an opt-in
  idempotency store keyed on the trigger's `Message-ID`. When a Postfix pipe
  re-delivers the same trigger (a slow-but-succeeding pipe gets re-queued), the
  filter suppresses the duplicate receipt instead of emailing it twice. Off by
  default; recommended for any pipe deployment.

- A `not_found` recipient whose `Message-ID` appears elsewhere in the log (e.g. an
  antivirus/scanner line) but has no delivery event is now annotated "message seen
  in the log, but no delivery event was recorded" — distinguishing it from a message
  with no trace at all. The outcome stays `not_found`; no delivery is implied. The
  JSON gains an optional per-recipient `note` field.

### Fixed
- A receipt that found no delivery (all `not_found`) no longer renders the remote
  delivery caveat ("a 'delivered' outcome means the remote mail server accepted the
  message (SMTP 2xx)…") — there is no delivery to qualify, so it now makes no
  remote/SMTP claim and states plainly that no delivery record was found.

## [0.5.0] - 2026-06-09

### Changed (breaking — JSON schema)
- The per-recipient `outcome` value `delivered` is **replaced** by `delivered_remote`
  (a remote SMTP/LMTP relay accepted the message at handoff to a remote host) and
  `delivered_local` (a local Postfix transport/pipe/mailbox accepted it). Consumers
  filtering on `outcome == "delivered"` must now match `delivered_remote` /
  `delivered_local` (e.g. `select(.outcome == "delivered_remote" or .outcome ==
  "delivered_local")`). The whole-email `summary` may be `delivered_remote`,
  `delivered_local`, `delivered` (all delivered across a mix of transports —
  summary-only), `bounced`, `deferred`, `not_found`, or `mixed`; `summary_counts`
  keys follow the per-recipient values.

### Fixed
- A message handed to a local transport (e.g. `relay=mailreceipt`, `postfix/pipe`)
  is no longer reported as "accepted by the remote mail server (SMTP 2xx)". A local
  delivery now renders "Delivered locally — accepted by a local mail transport" with
  a caveat that explicitly makes no remote-server or SMTP 2xx claim, and a mixed
  remote+local receipt uses a caveat covering both. The receipt names the handoff it
  actually observed.

## [0.4.1] - 2026-06-08

### Fixed
- `filter` replies now render a formal plain-text receipt (labeled `MAIL DELIVERY
  RECEIPT` sections, word outcomes like `DELIVERED` / `NOT FOUND`), quoted-printable
  encoded — so conservative desktop mail clients show a readable receipt instead of
  raw Markdown. The Markdown output of `check` is unchanged.
- Subjects carrying RFC 2047 encoded-words (e.g. KOI8-R or Windows-1251 Russian
  subjects) are now decoded to Unicode in the receipt instead of appearing as raw
  `=?koi8-r?...?=` text. Falls back to the raw header if a charset is unknown.
- `doctor --format` now rejects unknown values (exit 1) instead of silently emitting
  text, and accepts the `markdown` alias consistently with `check`. The error and
  `--help` advertise all accepted values: `md`, `markdown`, `json`.

### Changed
- Added a dependency on `golang.org/x/text` (the Go team's extended-text package),
  used only to decode legacy non-UTF-8 mail subject charsets.

## [0.4.0] - 2026-06-08

### Added
- `filter` command — a mail-filter front door. Forward a sent email (the original
  attached as `message/rfc822`) to an alias, and mailreceipt replies to the sender
  with a cited delivery receipt. Authorization is the core: it acts only on an
  MTA-authenticated internal envelope sender, only when the trigger sender shares a
  configured `receipt_filter` team with the attachment's `From`/`Sender`, and only
  when the attachment's Message-ID correlates to a real local-log line — the
  attachment is a selector, never evidence. Fails closed (silent drop) on any
  unmet gate, with a loop guard (`Auto-Submitted`, `Precedence: bulk`).
- `receipt_filter` config block in `.mailreceipt.yml` (domains, teams, reply_from).

### Fixed
- **Citation verification now requires an exact full-line match.** Previously a
  truncated or edited citation could pass `verify` as long as it was any substring
  of a real log line — weakening the tamper-evidence guarantee. Citations must now
  match a complete source log line.
- **Recipient fallback is now time-bounded or `not_found`, never unbounded.** A
  pasted thread with no Message-ID and no parsable date previously searched the
  whole log and could assert a false `delivered`/`bounced` from an unrelated event
  for the same recipient. With no Message-ID and no parsable send time, the outcome
  is now `not_found` — the honest answer. Lenient `Sent:`/`Date:` values are parsed
  to bound the recipient-window correlation.

### Security
- The `filter` reply path is hardened: random per-reply MIME boundary, strict
  single-mailbox parsing for trusted reply identities (rejecting quoted/obsolete
  local-parts before they reach a raw `From:` header), base64-encoded JSON
  attachment, and a documented trust-boundary "Security model" section. An empty
  `--envelope-from` is an explicit fail-closed drop.

## [0.3.0] - 2026-06-06

### Added
- `doctor` command — diagnoses a mail log (exists, readable, non-empty, timestamp
  format, delivery-line count) so a `not_found` result can be distinguished from an
  unreadable or wrong-format log. Emits an ANCC-shaped JSON report with `--format json`.
- `init` command — writes a `.mailreceipt.yml` with project defaults (`log`,
  `log_year`, `case_prefix`). `check` and `verify` read it as defaults; explicit
  flags always override.
- `docs/SKILL.md` and an ANCC-compliant CLI surface — mailreceipt now passes
  `ancc validate` (0 failures) and carries the ANCC badge.

## [0.2.0] - 2026-06-06

### Added
- `mixed` overall summary for receipts whose recipients resolved to more than one
  outcome (e.g. some delivered, some not found). The Markdown headline states the
  mix — `Overall: Mixed — 4 delivered, 1 not found` — and the JSON artifact gains
  a `summary_counts` object with the per-outcome tally.

### Fixed
- The overall headline no longer contradicts the per-recipient table. Previously a
  single `not_found` recipient collapsed the whole verdict to `Not found` even when
  other recipients were delivered; the summary now faithfully compresses the rows.

## [0.1.1] - 2026-06-06

### Fixed
- Parse RFC3339 / ISO-8601 syslog timestamps (e.g. `2026-06-05T14:09:36.750604+02:00`), the modern rsyslog default on Debian 12+ and most current distros. Previously only traditional BSD timestamps (`Jun  5 14:09:36`) parsed; an RFC3339 log produced zero events and reported every recipient as `not_found` even when the message was delivered. RFC3339 lines are self-dating and ignore `--log-year`.

## [0.1.0] - 2026-06-06

### Added
- Initial release: cited mail delivery receipts from Postfix logs.
- `check` — per-recipient delivered/bounced/deferred/not_found outcome with the verbatim log line as evidence; Markdown or JSON (`--format json`).
- `verify` — re-checks that every citation in a receipt still appears verbatim in the log.
- Message-ID correlation with recipient + time-window fallback; accepts an `.eml` file or stdin (RFC822 or a pasted top-of-thread block).
- `--case` reference stamping and `--log-year` for year-less syslog timestamps.
