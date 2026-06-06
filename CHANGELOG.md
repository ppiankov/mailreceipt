# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

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
