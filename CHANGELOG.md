# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [0.1.0] - 2026-06-06

### Added
- Initial release: cited mail delivery receipts from Postfix logs.
- `check` — per-recipient delivered/bounced/deferred/not_found outcome with the verbatim log line as evidence; Markdown or JSON (`--format json`).
- `verify` — re-checks that every citation in a receipt still appears verbatim in the log.
- Message-ID correlation with recipient + time-window fallback; accepts an `.eml` file or stdin (RFC822 or a pasted top-of-thread block).
- `--case` reference stamping and `--log-year` for year-less syslog timestamps.
