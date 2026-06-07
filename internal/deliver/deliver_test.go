package deliver

import (
	"strings"
	"testing"
	"time"

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
	e := eml.Email{
		To:   []string{"jdoe@exampleclient.test"},
		Date: time.Date(2026, 6, 5, 15, 9, 0, 0, time.UTC),
	}
	res := Analyze(e, parseLog(t, sampleLog))
	jdoe := find(res, "jdoe@exampleclient.test")
	if jdoe.Outcome != Delivered {
		t.Fatalf("fallback match: want delivered, got %s", jdoe.Outcome)
	}
	if jdoe.Match != MatchRecipient {
		t.Fatalf("want recipient_window match, got %s", jdoe.Match)
	}
}

func TestRecipientFallbackRequiresDateWhenNoMessageID(t *testing.T) {
	e := eml.Email{To: []string{"jdoe@exampleclient.test"}}
	res := Analyze(e, parseLog(t, sampleLog))
	jdoe := find(res, "jdoe@exampleclient.test")
	if jdoe.Outcome != NotFound {
		t.Fatalf("no-date fallback: want not_found, got %s", jdoe.Outcome)
	}
	if jdoe.Match != MatchNone {
		t.Fatalf("no-date fallback: want no match, got %s", jdoe.Match)
	}
	if jdoe.Citation != "" {
		t.Fatalf("not_found must carry no citation, got: %s", jdoe.Citation)
	}
}

func TestRecipientFallbackRejectsEventsOutsideWindow(t *testing.T) {
	e := eml.Email{
		To:   []string{"jdoe@exampleclient.test"},
		Date: time.Date(2026, 7, 5, 15, 9, 0, 0, time.UTC),
	}
	res := Analyze(e, parseLog(t, sampleLog))
	jdoe := find(res, "jdoe@exampleclient.test")
	if jdoe.Outcome != NotFound {
		t.Fatalf("outside-window fallback: want not_found, got %s", jdoe.Outcome)
	}
	if jdoe.Match != MatchNone {
		t.Fatalf("outside-window fallback: want no match, got %s", jdoe.Match)
	}
}

// resultWith builds a Result from bare per-recipient outcomes, for testing the
// Summary/Counts reduction directly.
func resultWith(outcomes ...Outcome) Result {
	var rr []RecipientResult
	for i, o := range outcomes {
		rr = append(rr, RecipientResult{Recipient: string(rune('a'+i)) + "@x.test", Outcome: o})
	}
	return Result{Recipients: rr}
}

func TestSummaryAllDelivered(t *testing.T) {
	if got := resultWith(Delivered, Delivered).Summary(); got != Delivered {
		t.Fatalf("all delivered -> delivered, got %s", got)
	}
}

func TestSummaryAllNotFound(t *testing.T) {
	if got := resultWith(NotFound, NotFound).Summary(); got != NotFound {
		t.Fatalf("all not_found -> not_found, got %s", got)
	}
}

func TestSummaryAnyBounceWins(t *testing.T) {
	if got := resultWith(Delivered, NotFound, Bounced).Summary(); got != Bounced {
		t.Fatalf("any bounce must surface over a mix, got %s", got)
	}
}

func TestSummaryDeferredOverMixNoBounce(t *testing.T) {
	if got := resultWith(Delivered, NotFound, Deferred).Summary(); got != Deferred {
		t.Fatalf("deferred (no bounce) must surface, got %s", got)
	}
}

func TestSummaryDeliveredPlusNotFoundIsMixed(t *testing.T) {
	// The incident case: 4 delivered + 1 not_found must be mixed, never not_found.
	got := resultWith(Delivered, Delivered, Delivered, Delivered, NotFound).Summary()
	if got != Mixed {
		t.Fatalf("delivered+not_found must be mixed, got %s", got)
	}
}

func TestSummaryNeverContradictsRows(t *testing.T) {
	// Invariant: if any recipient is delivered, the summary is never not_found.
	res := resultWith(Delivered, NotFound)
	if got := res.Summary(); got == NotFound {
		t.Fatalf("summary must not be not_found while a row is delivered, got %s", got)
	}
}

func TestCounts(t *testing.T) {
	c := resultWith(Delivered, Delivered, Delivered, Delivered, NotFound).Counts()
	if c[Delivered] != 4 || c[NotFound] != 1 {
		t.Fatalf("counts wrong: %+v", c)
	}
	if len(c) != 2 {
		t.Fatalf("only two distinct outcomes expected, got %d", len(c))
	}
}
