#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd "$(dirname "$0")" && pwd)"
ROOT="$(CDPATH= cd "$SCRIPT_DIR/../.." && pwd)"
WRAPPER="$ROOT/contrib/postfix/mailreceipt-postfix"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/mailreceipt-postfix-test.XXXXXX")"
trap 'rm -rf "$WORKDIR"' EXIT HUP INT TERM

mkdir -p "$WORKDIR/bin" "$WORKDIR/config" "$WORKDIR/debug" "$WORKDIR/tmp"
touch "$WORKDIR/mail.log"

cat > "$WORKDIR/bin/mailreceipt" <<'SH'
#!/bin/sh
set -eu
printf '%s\n' "$*" > "$MAILRECEIPT_TEST_ARGS"
cat > "$MAILRECEIPT_TEST_STDIN"
if [ "${MAILRECEIPT_TEST_FAIL:-0}" = "1" ]; then
  printf 'filter failed\n' >&2
  exit 1
fi
if [ "${MAILRECEIPT_TEST_REPLY:-1}" = "1" ]; then
  printf 'From: receipt@example.com\nTo: sender@example.com\n\nok\n'
fi
SH

cat > "$WORKDIR/bin/sendmail" <<'SH'
#!/bin/sh
set -eu
cat > "$MAILRECEIPT_TEST_SENT"
SH

chmod +x "$WORKDIR/bin/mailreceipt" "$WORKDIR/bin/sendmail"

cat > "$WORKDIR/input.eml" <<'EOF_INPUT'
From: sender@example.com
To: receipt@example.com
Subject: Test

body
EOF_INPUT

run_wrapper() {
  MAILRECEIPT_BIN="$WORKDIR/bin/mailreceipt" \
  SENDMAIL_BIN="$WORKDIR/bin/sendmail" \
  MAILRECEIPT_CONFIG_DIR="$WORKDIR/config" \
  MAILRECEIPT_LOG="$WORKDIR/mail.log" \
  MAILRECEIPT_DEBUG_DIR="$WORKDIR/debug" \
  MAILRECEIPT_TEST_ARGS="$WORKDIR/args" \
  MAILRECEIPT_TEST_STDIN="$WORKDIR/stdin" \
  MAILRECEIPT_TEST_SENT="$WORKDIR/sent" \
  MAILRECEIPT_POSTFIX_DEBUG="${MAILRECEIPT_POSTFIX_DEBUG:-0}" \
  TMPDIR="$WORKDIR/tmp" \
  "$WRAPPER" "$@"
}

run_wrapper sender@example.com < "$WORKDIR/input.eml"

grep -q '^filter --envelope-from sender@example.com --log ' "$WORKDIR/args"
grep -q '^Subject: Test$' "$WORKDIR/stdin"
grep -q '^ok$' "$WORKDIR/sent"

debug_files="$(find "$WORKDIR/debug" -type f | wc -l | tr -d ' ')"
if [ "$debug_files" != "0" ]; then
  printf 'debug files were created without opt-in\n' >&2
  exit 1
fi
tmp_files="$(find "$WORKDIR/tmp" -type f | wc -l | tr -d ' ')"
if [ "$tmp_files" != "0" ]; then
  printf 'temporary files were retained without debug opt-in\n' >&2
  exit 1
fi

MAILRECEIPT_POSTFIX_DEBUG=1 run_wrapper sender@example.com < "$WORKDIR/input.eml"

set -- "$WORKDIR"/debug/in.*
[ -f "$1" ] || {
  printf 'debug input file was not created\n' >&2
  exit 1
}
grep -q '^Subject: Test$' "$1"

set -- "$WORKDIR"/debug/err.*
[ -f "$1" ] || {
  printf 'debug stderr file was not created\n' >&2
  exit 1
}

printf 'ok\n'
