package maillog

import (
	"strings"
	"testing"
	"time"
)

const log = `Jun  5 02:37:11 mail01 postfix/cleanup[20111]: 4F1A2B3C01: message-id=<m1@ex.test>
Jun  5 02:37:14 mail01 postfix/smtp[20120]: 4F1A2B3C01: to=<a@client.test>, relay=mx.client.test[203.0.113.25]:25, status=sent (250 2.0.0 OK)
Jun  5 02:37:15 mail01 postfix/smtp[20121]: 4F1A2B3C01: to=<b@client.test>, relay=mx.client.test[203.0.113.25]:25, status=bounced (host said: 550 User unknown)
`

func TestParseLinksMessageIDToEvents(t *testing.T) {
	l := Parse(strings.NewReader(log), 2026)
	if len(l.Events) != 2 {
		t.Fatalf("want 2 delivery events, got %d", len(l.Events))
	}
	for _, e := range l.Events {
		if e.MessageID != "m1@ex.test" {
			t.Fatalf("event %q should be linked to m1@ex.test via cleanup line, got %q", e.To, e.MessageID)
		}
	}
}

func TestParseExtractsStatusAndResponse(t *testing.T) {
	l := Parse(strings.NewReader(log), 2026)
	a := l.EventsForRecipient("a@client.test", time.Time{}, time.Time{})
	if len(a) != 1 || a[0].Status != StatusSent {
		t.Fatalf("a@ should be sent, got %+v", a)
	}
	if !strings.Contains(a[0].Response, "250 2.0.0 OK") {
		t.Fatalf("response text should be captured, got %q", a[0].Response)
	}
	b := l.EventsForRecipient("b@client.test", time.Time{}, time.Time{})
	if len(b) != 1 || b[0].Status != StatusBounced {
		t.Fatalf("b@ should be bounced, got %+v", b)
	}
}

func TestParseIgnoresNonPostfixLines(t *testing.T) {
	noise := "Jun  5 02:00:00 mail01 systemd[1]: Started something.\n" + log
	l := Parse(strings.NewReader(noise), 2026)
	if len(l.Events) != 2 {
		t.Fatalf("noise line must be ignored, got %d events", len(l.Events))
	}
}

func TestRawLineIsPreservedVerbatim(t *testing.T) {
	l := Parse(strings.NewReader(log), 2026)
	for _, e := range l.Events {
		if !strings.HasPrefix(e.RawLine, "Jun  5") || !strings.Contains(e.RawLine, "status=") {
			t.Fatalf("raw line must be the verbatim source line, got %q", e.RawLine)
		}
	}
}

// rfc3339Log is a sanitized version of the 2026-06-06 incident: a modern rsyslog
// host (Debian 12+) writes RFC3339 timestamps. The KLMS line is non-postfix noise
// and must be ignored; the three smtp lines share one queue id and are all sent.
const rfc3339Log = `2026-06-05T14:09:33.208849+02:00 mail KLMS: clean: message-id="<m9@ex.test>": rcpt-to="a@client.test","b@client.test","c@client.test"
2026-06-05T14:09:33.300000+02:00 mail postfix/cleanup[3673400]: F3867160060B: message-id=<m9@ex.test>
2026-06-05T14:09:36.750604+02:00 mail postfix/smtp[3673461]: F3867160060B: to=<a@client.test>, relay=mx.client.test[52.101.10.14]:25, dsn=2.6.0, status=sent (250 2.6.0 Queued mail for delivery)
2026-06-05T14:09:36.750855+02:00 mail postfix/smtp[3673461]: F3867160060B: to=<b@client.test>, relay=mx.client.test[52.101.10.14]:25, dsn=2.6.0, status=sent (250 2.6.0 Queued mail for delivery)
2026-06-05T14:09:36.751041+02:00 mail postfix/smtp[3673461]: F3867160060B: to=<c@client.test>, relay=mx.client.test[52.101.10.14]:25, dsn=2.6.0, status=sent (250 2.6.0 Queued mail for delivery)
`

func TestParseRFC3339Timestamps(t *testing.T) {
	// year=0 deliberately: RFC3339 lines carry their own year and must ignore it.
	l := Parse(strings.NewReader(rfc3339Log), 0)
	if len(l.Events) != 3 {
		t.Fatalf("want 3 delivery events from RFC3339 log, got %d", len(l.Events))
	}
	for _, e := range l.Events {
		if e.Status != StatusSent {
			t.Fatalf("recipient %q should be sent, got %q", e.To, e.Status)
		}
		if e.MessageID != "m9@ex.test" {
			t.Fatalf("recipient %q should link to m9@ex.test via cleanup line, got %q", e.To, e.MessageID)
		}
	}
}

func TestParseRFC3339TimeIsSelfDated(t *testing.T) {
	// Pass an absurd year; RFC3339 must ignore it and use its own (2026).
	l := Parse(strings.NewReader(rfc3339Log), 1999)
	a := l.EventsForRecipient("a@client.test", time.Time{}, time.Time{})
	if len(a) != 1 {
		t.Fatalf("want 1 event for a@, got %d", len(a))
	}
	if y := a[0].Time.Year(); y != 2026 {
		t.Fatalf("RFC3339 timestamp should self-date to 2026, got year %d", y)
	}
	if off := a[0].Time.Format("-07:00"); off != "+02:00" {
		t.Fatalf("RFC3339 zone offset should be preserved (+02:00), got %s", off)
	}
}

func TestParseMixedTimestampFormats(t *testing.T) {
	mixed := log + rfc3339Log // BSD block (2 events) + RFC3339 block (3 events)
	l := Parse(strings.NewReader(mixed), 2026)
	if len(l.Events) != 5 {
		t.Fatalf("mixed-format log should yield 5 events, got %d", len(l.Events))
	}
}

// WO-38: the parsed delivery-event range bounds not_found receipts and doctor.
func TestLogTimeRange(t *testing.T) {
	l := Parse(strings.NewReader(rfc3339Log), 0)
	first, last, ok := l.TimeRange()
	if !ok {
		t.Fatal("time range should be present")
	}
	if first.Format(time.RFC3339Nano) != "2026-06-05T14:09:36.750604+02:00" {
		t.Fatalf("first: got %s", first.Format(time.RFC3339Nano))
	}
	if last.Format(time.RFC3339Nano) != "2026-06-05T14:09:36.751041+02:00" {
		t.Fatalf("last: got %s", last.Format(time.RFC3339Nano))
	}
}

// WO-34: Dovecot LDA/LMTP local mailbox deliveries (the common Postfix+Dovecot
// internal-delivery path) are parsed as delivery events.
func TestParseDovecotLDADelivery(t *testing.T) {
	const log = `2026-06-09T09:28:52+02:00 mail dovecot: lda(a.user)<4050777><6SQCDm4XKGpZzz0ASWwcBg>: msgid=<dv-1@example.test>: saved mail to INBOX
`
	l := Parse(strings.NewReader(log), 2026)
	if len(l.Events) != 1 {
		t.Fatalf("dovecot lda save should yield 1 event, got %d", len(l.Events))
	}
	e := l.Events[0]
	if e.Daemon != "dovecot" || e.Status != StatusSent || e.To != "a.user" || e.MessageID != "dv-1@example.test" {
		t.Fatalf("dovecot event fields wrong: %+v", e)
	}
	if e.Response != "saved mail to INBOX" {
		t.Fatalf("dovecot response should name saved mailbox, got %q", e.Response)
	}
}

func TestParseDovecotLMTPDelivery(t *testing.T) {
	const log = `2026-06-09T10:00:00+02:00 mail dovecot: lmtp(4242, auser@example.test): msgid=<dv-2@example.test>: saved mail to INBOX/Maildir
`
	l := Parse(strings.NewReader(log), 2026)
	if len(l.Events) != 1 || l.Events[0].To != "auser@example.test" || l.Events[0].MessageID != "dv-2@example.test" {
		t.Fatalf("dovecot lmtp parse wrong: %+v", l.Events)
	}
}

// WO-40: Dovecot sieve local-store lines use "stored mail into mailbox", not the
// WO-34 "saved mail to" marker. This is still a successful local handoff.
func TestParseDovecotSieveStoredMailIntoMailboxDelivery(t *testing.T) {
	const log = `2026-07-02T08:12:01+02:00 mail dovecot: lda(clerk)<4050777><6SQCDm4XKGpZzz0ASWwcBg>: sieve: msgid=<sieve-store@example.test>: stored mail into mailbox 'INBOX'
`
	l := Parse(strings.NewReader(log), 2026)
	if len(l.Events) != 1 {
		t.Fatalf("dovecot sieve store should yield 1 event, got %d", len(l.Events))
	}
	e := l.Events[0]
	if e.Daemon != "dovecot" || e.Status != StatusSent || e.To != "clerk" || e.MessageID != "sieve-store@example.test" {
		t.Fatalf("dovecot sieve event fields wrong: %+v", e)
	}
	if e.Response != "stored mail into mailbox INBOX" {
		t.Fatalf("dovecot sieve response should name stored mailbox, got %q", e.Response)
	}
}

func TestDovecotNonStoreSieveLinesAreNotDeliveries(t *testing.T) {
	const log = `2026-07-02T08:12:01+02:00 mail dovecot: lda(clerk)<4050777><6SQCDm4XKGpZzz0ASWwcBg>: sieve: msgid=<sieve-forward@example.test>: forwarded to <archive@example.test>
2026-07-02T08:12:02+02:00 mail dovecot: lda(jsmith)<4050778><7SQCDm4XKGpZzz0ASWwcBg>: sieve: msgid=<sieve-discard@example.test>: discarded
2026-07-02T08:12:03+02:00 mail dovecot: lda(clerk): sieve: msgid=<sieve-marked-discard@example.test>: marked message to be discarded
`
	if l := Parse(strings.NewReader(log), 2026); len(l.Events) != 0 {
		t.Fatalf("non-store dovecot lines must not be deliveries, got %+v", l.Events)
	}
}
