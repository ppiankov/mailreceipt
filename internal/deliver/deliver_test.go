package deliver

import (
	"strings"
	"testing"

	"github.com/ppiankov/mailreceipt/internal/eml"
	"github.com/ppiankov/mailreceipt/internal/maillog"
)

// sampleLog mirrors testdata/mail.log: jdoe deferred then sent, team bounced.
const sampleLog = `Jun  5 15:09:02 mail01 postfix/cleanup[20420]: 7C2D9E1F02: message-id=<reminder-1509@example-ip.test>
Jun  5 15:09:20 mail01 postfix/smtp[20440]: 7C2D9E1F02: to=<jdoe@exampleclient.test>, relay=mx.exampleclient.test[203.0.113.25]:25, status=deferred (connect to mx.exampleclient.test[203.0.113.25]:25: Connection timed out)
Jun  5 15:09:21 mail01 postfix/smtp[20441]: 7C2D9E1F02: to=<team@exampleclient.test>, relay=mx.exampleclient.test[203.0.113.25]:25, status=bounced (host mx.exampleclient.test[203.0.113.25] said: 550 5.1.1 User unknown)
Jun  5 15:41:55 mail01 postfix/smtp[20460]: 7C2D9E1F02: to=<jdoe@exampleclient.test>, relay=mx.exampleclient.test[203.0.113.25]:25, status=sent (250 2.0.0 OK: queued as D4E5F6)
`

func parseLog(t *testing.T, s string) maillog.Log {
	t.Helper()
	return maillog.Parse(strings.NewReader(s), 2026)
}

func find(res Result, rcpt string) RecipientResult {
	for _, r := range res.Recipients {
		if r.Recipient == rcpt {
			return r
		}
	}
	return RecipientResult{}
}

func TestDeferredThenSentResolvesToDelivered(t *testing.T) {
	e := eml.Email{
		MessageID: "reminder-1509@example-ip.test",
		To:        []string{"jdoe@exampleclient.test"},
		Cc:        []string{"team@exampleclient.test"},
	}
	res := Analyze(e, parseLog(t, sampleLog))

	jdoe := find(res, "jdoe@exampleclient.test")
	if jdoe.Outcome != Delivered {
		t.Fatalf("jdoe: want delivered (later sent supersedes deferred), got %s", jdoe.Outcome)
	}
	if !strings.Contains(jdoe.Citation, "status=sent") {
		t.Fatalf("jdoe citation should be the sent line, got: %s", jdoe.Citation)
	}
	if jdoe.Match != MatchMessageID {
		t.Fatalf("jdoe should match by message_id, got %s", jdoe.Match)
	}
}

func TestBouncedRecipient(t *testing.T) {
	e := eml.Email{
		MessageID: "reminder-1509@example-ip.test",
		To:        []string{"team@exampleclient.test"},
	}
	res := Analyze(e, parseLog(t, sampleLog))
	team := find(res, "team@exampleclient.test")
	if team.Outcome != Bounced {
		t.Fatalf("team: want bounced, got %s", team.Outcome)
	}
	if !strings.Contains(team.Citation, "User unknown") {
		t.Fatalf("team citation should carry the bounce reason, got: %s", team.Citation)
	}
}

func TestSummaryBouncedWins(t *testing.T) {
	e := eml.Email{
		MessageID: "reminder-1509@example-ip.test",
		To:        []string{"jdoe@exampleclient.test"},
		Cc:        []string{"team@exampleclient.test"},
	}
	res := Analyze(e, parseLog(t, sampleLog))
	if got := res.Summary(); got != Bounced {
		t.Fatalf("overall summary: a bounce must surface, got %s", got)
	}
}

func TestNotFoundIsNotDelivered(t *testing.T) {
	e := eml.Email{
		MessageID: "ghost@example-ip.test",
		To:        []string{"nobody@elsewhere.test"},
	}
	res := Analyze(e, parseLog(t, sampleLog))
	r := find(res, "nobody@elsewhere.test")
	if r.Outcome != NotFound {
		t.Fatalf("absent recipient must be not_found, got %s", r.Outcome)
	}
	if r.Citation != "" {
		t.Fatalf("not_found must carry no citation, got: %s", r.Citation)
	}
}

func TestCaveatAlwaysPresent(t *testing.T) {
	res := Analyze(eml.Email{To: []string{"x@y.test"}}, parseLog(t, sampleLog))
	if !strings.Contains(res.Caveat, "transport, not attention") {
		t.Fatal("the transport-not-attention caveat must always be present")
	}
}

func TestRecipientFallbackWhenNoMessageID(t *testing.T) {
	// No message-id on the email; must fall back to recipient matching.
	e := eml.Email{To: []string{"jdoe@exampleclient.test"}}
	res := Analyze(e, parseLog(t, sampleLog))
	jdoe := find(res, "jdoe@exampleclient.test")
	if jdoe.Outcome != Delivered {
		t.Fatalf("fallback match: want delivered, got %s", jdoe.Outcome)
	}
	if jdoe.Match != MatchRecipient {
		t.Fatalf("want recipient_window match, got %s", jdoe.Match)
	}
}
