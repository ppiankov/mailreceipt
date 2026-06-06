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
