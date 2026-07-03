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
	if jdoe.Outcome != DeliveredRemote {
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
	if jdoe.Outcome != DeliveredRemote {
		t.Fatalf("fallback match: want delivered, got %s", jdoe.Outcome)
	}
	// WO-42: a no-Message-ID recipient may resolve via the recipient-set match
	// (unique queue-id) or the per-recipient window; both are recipient-based
	// fallbacks, not a Message-ID match.
	if jdoe.Match != MatchRecipient && jdoe.Match != MatchRecipientSet {
		t.Fatalf("want a recipient-based fallback match, got %s", jdoe.Match)
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

func TestPastedUnparseableDateReturnsNotFoundEndToEnd(t *testing.T) {
	// WO-15: raw pasted input must not reopen unbounded recipient fallback.
	raw := `From: Anna Petrova <anna@ip.test>
Sent: definitely not a date
To: jdoe@exampleclient.test
Subject: matter

Body
`
	e, err := eml.Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if e.MessageID != "" {
		t.Fatalf("pasted block has no message-id, got %q", e.MessageID)
	}
	if !e.Date.IsZero() {
		t.Fatalf("unparseable pasted date should leave date zero, got %s", e.Date)
	}
	res := Analyze(e, parseLog(t, sampleLog))
	jdoe := find(res, "jdoe@exampleclient.test")
	if jdoe.Outcome != NotFound {
		t.Fatalf("raw pasted unparseable date: want not_found, got %s", jdoe.Outcome)
	}
	if jdoe.Match != MatchNone {
		t.Fatalf("raw pasted unparseable date: want no match, got %s", jdoe.Match)
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
	// All remote-delivered -> the remote subtype is the verdict.
	if got := resultWith(DeliveredRemote, DeliveredRemote).Summary(); got != DeliveredRemote {
		t.Fatalf("all delivered_remote -> delivered_remote, got %s", got)
	}
	// All local-delivered -> the local subtype.
	if got := resultWith(DeliveredLocal, DeliveredLocal).Summary(); got != DeliveredLocal {
		t.Fatalf("all delivered_local -> delivered_local, got %s", got)
	}
	// All delivered but mixed transports -> the summary-only Delivered (success).
	if got := resultWith(DeliveredRemote, DeliveredLocal).Summary(); got != Delivered {
		t.Fatalf("remote+local all-delivered -> delivered, got %s", got)
	}
}

func TestSummaryAllNotFound(t *testing.T) {
	if got := resultWith(NotFound, NotFound).Summary(); got != NotFound {
		t.Fatalf("all not_found -> not_found, got %s", got)
	}
}

func TestSummaryAnyBounceWins(t *testing.T) {
	if got := resultWith(DeliveredRemote, NotFound, Bounced).Summary(); got != Bounced {
		t.Fatalf("any bounce must surface over a mix, got %s", got)
	}
}

func TestSummaryDeferredOverMixNoBounce(t *testing.T) {
	if got := resultWith(DeliveredRemote, NotFound, Deferred).Summary(); got != Deferred {
		t.Fatalf("deferred (no bounce) must surface, got %s", got)
	}
}

func TestSummaryDeliveredPlusNotFoundIsMixed(t *testing.T) {
	// The incident case: 4 delivered + 1 not_found must be mixed, never not_found.
	got := resultWith(DeliveredRemote, DeliveredRemote, DeliveredRemote, DeliveredRemote, NotFound).Summary()
	if got != Mixed {
		t.Fatalf("delivered+not_found must be mixed, got %s", got)
	}
}

func TestSummaryNeverContradictsRows(t *testing.T) {
	// Invariant: if any recipient is delivered, the summary is never not_found.
	res := resultWith(DeliveredRemote, NotFound)
	if got := res.Summary(); got == NotFound {
		t.Fatalf("summary must not be not_found while a row is delivered, got %s", got)
	}
}

func TestCounts(t *testing.T) {
	c := resultWith(DeliveredRemote, DeliveredRemote, DeliveredRemote, DeliveredRemote, NotFound).Counts()
	if c[DeliveredRemote] != 4 || c[NotFound] != 1 {
		t.Fatalf("counts wrong: %+v", c)
	}
	if len(c) != 2 {
		t.Fatalf("only two distinct outcomes expected, got %d", len(c))
	}
}

// WO-27: the real incident line — a local Postfix pipe handoff (relay=mailreceipt,
// postfix/pipe) must classify as delivered_local, never delivered_remote, and the
// receipt must never claim a remote mail server or SMTP 2xx for it.
const localPipeLog = `2026-06-08T19:08:55.038187+02:00 mail postfix/cleanup[3955455]: 90D9F160065F: message-id=<local-1@acme.test>
2026-06-08T19:08:55.038187+02:00 mail postfix/pipe[3955456]: 90D9F160065F: to=<receipt@acme.test>, relay=mailreceipt, delay=1.9, delays=1.9/0/0/0.02, dsn=2.0.0, status=sent (delivered via mailreceipt service)
`

func TestLocalPipeHandoffIsDeliveredLocal(t *testing.T) {
	e := eml.Email{MessageID: "local-1@acme.test", To: []string{"receipt@acme.test"}}
	res := Analyze(e, parseLog(t, localPipeLog))
	rr := find(res, "receipt@acme.test")
	if rr.Outcome != DeliveredLocal {
		t.Fatalf("relay=mailreceipt pipe handoff must be delivered_local, got %s", rr.Outcome)
	}
	// WO-27 rev4: a local-only receipt must not render the literal phrases at all,
	// not even in a negating clause (a legal exhibit should not surface them).
	for _, bad := range []string{"remote mail server", "SMTP 2xx"} {
		if strings.Contains(res.Caveat, bad) {
			t.Fatalf("local-only caveat must not contain %q, got: %s", bad, res.Caveat)
		}
	}
	if !strings.Contains(res.Caveat, "delivered local") {
		t.Fatalf("local-only receipt should use the local caveat, got: %s", res.Caveat)
	}
}

// WO-27 rev4: a mixed remote+local receipt must not use a remote-only caveat that
// would describe the local recipient as remote SMTP acceptance, nor a local-only
// caveat that would understate the remote recipient.
func TestMixedRemoteLocalCaveatCoversBoth(t *testing.T) {
	c := caveatFor([]RecipientResult{
		{Recipient: "a@x.test", Outcome: DeliveredRemote},
		{Recipient: "b@x.test", Outcome: DeliveredLocal},
	})
	if c == remoteCaveat {
		t.Fatal("mixed remote+local must NOT use the remote-only caveat")
	}
	if c == localCaveat {
		t.Fatal("mixed remote+local must NOT use the local-only caveat")
	}
	// The mixed caveat scopes each claim; it must not flatly assert SMTP 2xx for all.
	if strings.Contains(c, "accepted the message (SMTP 2xx)") {
		t.Fatalf("mixed caveat must not globally assert SMTP 2xx, got: %s", c)
	}
}

func TestRemoteSmtpHandoffIsDeliveredRemote(t *testing.T) {
	// A normal remote smtp line (relay=mx.host[ip], status=sent (250 ...)).
	e := eml.Email{MessageID: "reminder-1509@example-ip.test", To: []string{"jdoe@exampleclient.test"}}
	res := Analyze(e, parseLog(t, sampleLog))
	rr := find(res, "jdoe@exampleclient.test")
	if rr.Outcome != DeliveredRemote {
		t.Fatalf("remote smtp handoff must be delivered_remote, got %s", rr.Outcome)
	}
	if !strings.Contains(res.Caveat, "remote mail server") {
		t.Fatalf("remote receipt should keep the remote-server caveat, got: %s", res.Caveat)
	}
}

// WO-33: a not_found whose message-id appears only in a non-delivery line (e.g. an
// antivirus scanner record) is annotated as "seen but not delivered", not bare.
func TestNotFoundSeenInScannerLineGetsNote(t *testing.T) {
	log := parseLog(t, `2026-06-09T09:28:51+02:00 mail KLMS: not processed: message-id="<seen-1@acme.test>": action="Skipped": rcpt-to="auser@acme.test"
2026-06-09T09:30:00+02:00 mail postfix/smtp[1]: AAAAAA: to=<other@x.test>, relay=mx[1.2.3.4]:25, status=sent (250 OK)
`)
	res := Analyze(eml.Email{MessageID: "seen-1@acme.test", To: []string{"auser@acme.test"}}, log)
	rr := find(res, "auser@acme.test")
	if rr.Outcome != NotFound {
		t.Fatalf("scanner-only message must stay not_found, got %s", rr.Outcome)
	}
	if rr.Note == "" {
		t.Fatal("a message seen in a non-delivery line should carry a 'seen but not delivered' note")
	}
}

func TestNotFoundNoTraceHasNoNote(t *testing.T) {
	log := parseLog(t, `2026-06-09T09:30:00+02:00 mail postfix/smtp[1]: AAAAAA: to=<other@x.test>, relay=mx[1.2.3.4]:25, status=sent (250 OK)
`)
	res := Analyze(eml.Email{MessageID: "absent@acme.test", To: []string{"nobody@acme.test"}}, log)
	rr := find(res, "nobody@acme.test")
	if rr.Outcome != NotFound || rr.Note != "" {
		t.Fatalf("a message with no trace must be bare not_found, got outcome=%s note=%q", rr.Outcome, rr.Note)
	}
}

// WO-34: an internal message delivered by Dovecot (Postfix mailbox_command=dovecot-lda)
// resolves to delivered_local, not not_found.
func TestDovecotInternalDeliveryIsDeliveredLocal(t *testing.T) {
	log := parseLog(t, `2026-06-09T09:28:51+02:00 mail KLMS: not processed: message-id="<dv1@acme.test>": rcpt-to="a.user@acme.test"
2026-06-09T09:28:52+02:00 mail dovecot: lda(a.user)<4050777><6SQCDm4XKGpZzz0ASWwcBg>: msgid=<dv1@acme.test>: saved mail to INBOX
`)
	res := Analyze(eml.Email{MessageID: "dv1@acme.test", To: []string{"a.user@acme.test"}}, log)
	rr := find(res, "a.user@acme.test")
	if rr.Outcome != DeliveredLocal {
		t.Fatalf("dovecot internal delivery must be delivered_local, got %s", rr.Outcome)
	}
	if strings.Contains(res.Caveat, "accepted the message (SMTP 2xx)") {
		t.Fatalf("dovecot local delivery must not affirm SMTP 2xx, got: %s", res.Caveat)
	}
}

// WO-34: /etc/aliases remaps an address to an unrelated mailbox username; the
// Dovecot save logs the mailbox name, not the address. For a SOLE recipient, the
// Message-ID join attributes the delivery correctly despite the name mismatch.
func TestDovecotAliasRemappedSoleRecipientMatchesByMessageID(t *testing.T) {
	log := parseLog(t, `2026-06-09T15:38:54+02:00 mail dovecot: lda(clerk)<4050777><sess>: msgid=<alias1@p.com>: saved mail to INBOX
`)
	res := Analyze(eml.Email{MessageID: "alias1@p.com", To: []string{"r.jones@acme.test"}}, log)
	rr := find(res, "r.jones@acme.test")
	if rr.Outcome != DeliveredLocal || rr.Match != MatchMessageID {
		t.Fatalf("aliased sole-recipient dovecot save should be delivered_local by message_id, got outcome=%s match=%s", rr.Outcome, rr.Match)
	}
}

// WO-34: with MULTIPLE recipients, an aliased Dovecot save whose mailbox name
// matches none of them must NOT be mis-attributed — honesty over a guess.
func TestDovecotAliasRemappedMultiRecipientDoesNotMisattribute(t *testing.T) {
	log := parseLog(t, `2026-06-09T15:38:54+02:00 mail dovecot: lda(clerk)<1><s>: msgid=<m1@p.com>: saved mail to INBOX
`)
	res := Analyze(eml.Email{MessageID: "m1@p.com", To: []string{"a@acme.test", "b@acme.test"}}, log)
	for _, r := range res.Recipients {
		if r.Outcome != NotFound {
			t.Fatalf("ambiguous aliased save must not be attributed; %s got %s", r.Recipient, r.Outcome)
		}
	}
}

// WO-35: a /etc/aliases redirect (j.smith -> docketing mailbox) delivered
// by postfix/local to maildrop. Postfix logs orig_to=<address> alongside
// to=<alias-target>, so the delivery correlates to the real recipient via orig_to —
// no /etc/aliases parsing. Fixture is jsmith's verbatim production line.
func TestAliasDeliveryCorrelatesViaOrigTo(t *testing.T) {
	log := parseLog(t, `2026-06-08T19:35:27+02:00 mail postfix/cleanup[3957651]: ABCD1234EF01: message-id=<aaaa1111@acme.test>
2026-06-08T19:35:28+02:00 mail postfix/local[3957653]: ABCD1234EF01: to=<docketing@mail.acme.test>, orig_to=<j.smith@acme.test>, relay=local, delay=3.3, delays=3.3/0/0/0.01, dsn=2.0.0, status=sent (delivered to command: /usr/bin/maildrop -d ${USER})
`)
	res := Analyze(eml.Email{MessageID: "aaaa1111@acme.test", To: []string{"j.smith@acme.test"}}, log)
	rr := find(res, "j.smith@acme.test")
	if rr.Outcome != DeliveredLocal {
		t.Fatalf("alias delivery via orig_to must be delivered_local, got %s", rr.Outcome)
	}
	if rr.Match != MatchMessageID {
		t.Fatalf("should correlate by message_id, got %s", rr.Match)
	}
	if strings.Contains(res.Caveat, "accepted the message (SMTP 2xx)") {
		t.Fatalf("local maildrop delivery must not affirm SMTP 2xx: %s", res.Caveat)
	}
}

// WO-42: a forwarded message whose Message-ID was STRIPPED (Outlook) resolves all
// recipients via a unique recipient-SET match, including recipients who also
// received unrelated mail in the window (which per-recipient uniqueness rejects).
func TestRecipientSetMatchStrippedMessageID(t *testing.T) {
	// multi-recipient send: queue Q2 delivers to all three; alice ALSO got another
	// message (QOTHER) the same day, which would defeat per-recipient uniqueness.
	log := parseLog(t, `Jun 19 15:49:30 mail01 postfix/smtp[5001]: AAAA0019: to=<alice@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 OK other)
Jun 19 14:50:08 mail01 postfix/smtp[5002]: BBBB0019: to=<alice@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 OK set)
Jun 19 14:50:08 mail01 postfix/smtp[5002]: BBBB0019: to=<bob@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 OK set)
Jun 19 14:50:08 mail01 postfix/smtp[5002]: BBBB0019: to=<carol@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 OK set)
`)
	date, _ := time.Parse(time.RFC1123Z, "Fri, 19 Jun 2026 15:49:35 +0000")
	e := eml.Email{To: []string{"alice@clientfirm.test", "bob@clientfirm.test", "carol@clientfirm.test"}, Date: date}
	res := Analyze(e, log)
	for _, rcpt := range []string{"alice@clientfirm.test", "bob@clientfirm.test", "carol@clientfirm.test"} {
		r := find(res, rcpt)
		if r.Outcome != DeliveredRemote {
			t.Fatalf("%s: want delivered_remote via set match, got %s (match=%s)", rcpt, r.Outcome, r.Match)
		}
		if r.Match != MatchRecipientSet {
			t.Fatalf("%s: want recipient_set match, got %s", rcpt, r.Match)
		}
	}
}

// WO-42: a forwarded message whose Message-ID is PRESENT but matches no log event
// (Exchange rewrites the id between the Sent copy and the wire) still resolves via
// the recipient-set match — set-match engages when the id fails to correlate.
func TestRecipientSetMatchRewrittenMessageID(t *testing.T) {
	log := parseLog(t, `Jul  2 17:33:15 mail01 postfix/cleanup[6000]: CCCC0002: message-id=<onwire-id@example.test>
Jul  2 17:33:16 mail01 postfix/smtp[6001]: CCCC0002: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 OK a)
Jul  2 17:33:16 mail01 postfix/smtp[6001]: CCCC0002: to=<b@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 OK b)
`)
	date, _ := time.Parse(time.RFC1123Z, "Thu, 02 Jul 2026 17:33:00 +0000")
	// The forwarded copy carries a DIFFERENT id than the wire (Exchange rewrite).
	e := eml.Email{MessageID: "sentcopy-id@example.test", To: []string{"a@clientfirm.test", "b@clientfirm.test"}, Date: date}
	res := Analyze(e, log)
	for _, rcpt := range []string{"a@clientfirm.test", "b@clientfirm.test"} {
		r := find(res, rcpt)
		if r.Outcome != DeliveredRemote || r.Match != MatchRecipientSet {
			t.Fatalf("%s: want delivered_remote via recipient_set, got %s (match=%s)", rcpt, r.Outcome, r.Match)
		}
	}
}

// WO-42: when TWO different messages in the window both cover the recipient set,
// the match is ambiguous and every recipient stays not_found (never guess).
func TestRecipientSetMatchAmbiguousStaysNotFound(t *testing.T) {
	log := parseLog(t, `Jun 19 14:00:00 mail01 postfix/smtp[7001]: DDDD0019: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 first a)
Jun 19 14:00:00 mail01 postfix/smtp[7001]: DDDD0019: to=<b@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 first b)
Jun 19 16:00:00 mail01 postfix/smtp[7002]: EEEE0019: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 second a)
Jun 19 16:00:00 mail01 postfix/smtp[7002]: EEEE0019: to=<b@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 second b)
`)
	date, _ := time.Parse(time.RFC1123Z, "Fri, 19 Jun 2026 15:00:00 +0000")
	e := eml.Email{To: []string{"a@clientfirm.test", "b@clientfirm.test"}, Date: date}
	res := Analyze(e, log)
	for _, rcpt := range []string{"a@clientfirm.test", "b@clientfirm.test"} {
		if r := find(res, rcpt); r.Outcome != NotFound {
			t.Fatalf("%s: ambiguous set (two messages) must stay not_found, got %s (match=%s)", rcpt, r.Outcome, r.Match)
		}
	}
}

// WO-42 rev: a mixed-transport message (remote SMTP + local Dovecot) whose
// Message-ID does not match the forward resolves ALL recipients — including the
// local one — via a unique recipient-set match keyed on message-id, not queue-id.
func TestRecipientSetMatchMixedRemoteAndDovecot(t *testing.T) {
	log := parseLog(t, `Jul  2 17:33:14 mail01 postfix/qmgr[900]: A1AAAA02: from=<sender@example.test>, size=1000, nrcpt=3 (queue active)
Jul  2 17:33:15 mail01 postfix/cleanup[6000]: A1AAAA02: message-id=<wire@example.test>
Jul  2 17:33:16 mail01 postfix/smtp[6001]: A1AAAA02: to=<remote1@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 OK)
Jul  2 17:33:16 mail01 postfix/smtp[6001]: A1AAAA02: to=<remote2@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 OK)
Jul  2 17:33:17 mail01 dovecot: lda(localuser)<999><sess>: sieve: msgid=<wire@example.test>: stored mail into mailbox 'INBOX'
`)
	date, _ := time.Parse(time.RFC1123Z, "Thu, 02 Jul 2026 17:33:00 +0000")
	e := eml.Email{From: "Sender <sender@example.test>", To: []string{"remote1@clientfirm.test", "remote2@clientfirm.test", "localuser@example.test"}, Date: date}
	res := Analyze(e, log)
	want := map[string]Outcome{
		"remote1@clientfirm.test": DeliveredRemote,
		"remote2@clientfirm.test": DeliveredRemote,
		"localuser@example.test":  DeliveredLocal,
	}
	for rcpt, wantOutcome := range want {
		r := find(res, rcpt)
		if r.Outcome != wantOutcome || r.Match != MatchRecipientSet {
			t.Fatalf("%s: want %s via recipient_set, got %s (match=%s)", rcpt, wantOutcome, r.Outcome, r.Match)
		}
	}
}

// WO-42 rev: two messages in the window cover the same recipient set but were sent
// by DIFFERENT senders; only the candidate whose sender matches the forwarded
// From may resolve. The other must not be attributed.
func TestRecipientSetMatchSenderDisambiguates(t *testing.T) {
	log := parseLog(t, `Jun 19 14:00:00 mail01 postfix/qmgr[900]: A1BBBB19: from=<other@example.test>, size=1, nrcpt=2 (queue active)
Jun 19 14:00:01 mail01 postfix/cleanup[900]: A1BBBB19: message-id=<other-msg@example.test>
Jun 19 14:00:02 mail01 postfix/smtp[901]: A1BBBB19: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 other a)
Jun 19 14:00:02 mail01 postfix/smtp[901]: A1BBBB19: to=<b@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 other b)
Jun 19 15:00:00 mail01 postfix/qmgr[900]: A1CCCC19: from=<sender@example.test>, size=1, nrcpt=2 (queue active)
Jun 19 15:00:01 mail01 postfix/cleanup[900]: A1CCCC19: message-id=<mine-msg@example.test>
Jun 19 15:00:02 mail01 postfix/smtp[902]: A1CCCC19: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 mine a)
Jun 19 15:00:02 mail01 postfix/smtp[902]: A1CCCC19: to=<b@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 mine b)
`)
	date, _ := time.Parse(time.RFC1123Z, "Fri, 19 Jun 2026 15:00:30 +0000")
	e := eml.Email{From: "Sender <sender@example.test>", To: []string{"a@clientfirm.test", "b@clientfirm.test"}, Date: date}
	res := Analyze(e, log)
	for _, rcpt := range []string{"a@clientfirm.test", "b@clientfirm.test"} {
		r := find(res, rcpt)
		if r.Outcome != DeliveredRemote || r.Match != MatchRecipientSet {
			t.Fatalf("%s: sender-matched candidate should resolve, got %s (match=%s)", rcpt, r.Outcome, r.Match)
		}
		// Must be attributed to the sender's message (250 mine ...), not the other.
		if !strings.Contains(r.Response, "mine") {
			t.Fatalf("%s: attributed to wrong sender's message: %q", rcpt, r.Response)
		}
	}
}

// WO-42 rev: the only recipient-set candidate in the window was sent by a
// DIFFERENT sender than the forwarded message; it must not be attributed.
func TestRecipientSetMatchWrongSenderNotAttributed(t *testing.T) {
	log := parseLog(t, `Jun 19 15:00:00 mail01 postfix/qmgr[900]: A1DDDD19: from=<other@example.test>, size=1, nrcpt=2 (queue active)
Jun 19 15:00:01 mail01 postfix/cleanup[900]: A1DDDD19: message-id=<other-msg@example.test>
Jun 19 15:00:02 mail01 postfix/smtp[901]: A1DDDD19: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 other a)
Jun 19 15:00:02 mail01 postfix/smtp[901]: A1DDDD19: to=<b@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 other b)
`)
	date, _ := time.Parse(time.RFC1123Z, "Fri, 19 Jun 2026 15:00:30 +0000")
	e := eml.Email{From: "Sender <sender@example.test>", To: []string{"a@clientfirm.test", "b@clientfirm.test"}, Date: date}
	res := Analyze(e, log)
	for _, rcpt := range []string{"a@clientfirm.test", "b@clientfirm.test"} {
		if r := find(res, rcpt); r.Outcome != NotFound {
			t.Fatalf("%s: wrong-sender candidate must not be attributed, got %s (match=%s)", rcpt, r.Outcome, r.Match)
		}
	}
}

// WO-42 rev-5 (Finding 3, safety): a multi-recipient forward with a KNOWN sender
// must NOT fall to the per-recipient window when candidate deliveries carry no
// sender to confirm. It stays not_found rather than attribute without the sender
// component of the join key.
func TestRecipientSetMatchKnownSenderNoConfirmationStaysNotFound(t *testing.T) {
	log := parseLog(t, `Jun 19 15:00:02 mail01 postfix/smtp[901]: A1A0AA19: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 a)
Jun 19 15:00:02 mail01 postfix/smtp[901]: A1A0AA19: to=<b@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 b)
`)
	date, _ := time.Parse(time.RFC1123Z, "Fri, 19 Jun 2026 15:00:30 +0000")
	e := eml.Email{From: "Sender <sender@example.test>", To: []string{"a@clientfirm.test", "b@clientfirm.test"}, Date: date}
	res := Analyze(e, log)
	for _, rcpt := range []string{"a@clientfirm.test", "b@clientfirm.test"} {
		if r := find(res, rcpt); r.Outcome != NotFound {
			t.Fatalf("%s: known sender + unconfirmable candidate must stay not_found, got %s (match=%s)", rcpt, r.Outcome, r.Match)
		}
	}
}

// WO-42 rev-5 (Finding 2, delegated): the forwarded message's From and Sender are
// both sender candidates. A send-on-behalf message (From: principal, Sender:
// assistant) whose logged envelope sender is the assistant must resolve.
func TestRecipientSetMatchDelegatedSenderResolves(t *testing.T) {
	log := parseLog(t, `Jun 19 15:00:00 mail01 postfix/qmgr[900]: A1B0BB19: from=<assistant@example.test>, size=1, nrcpt=2 (queue active)
Jun 19 15:00:01 mail01 postfix/cleanup[900]: A1B0BB19: message-id=<wire@example.test>
Jun 19 15:00:02 mail01 postfix/smtp[901]: A1B0BB19: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 a)
Jun 19 15:00:02 mail01 postfix/smtp[901]: A1B0BB19: to=<b@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 b)
`)
	date, _ := time.Parse(time.RFC1123Z, "Fri, 19 Jun 2026 15:00:30 +0000")
	e := eml.Email{From: "Principal <principal@example.test>", Sender: "Assistant <assistant@example.test>", To: []string{"a@clientfirm.test", "b@clientfirm.test"}, Date: date}
	res := Analyze(e, log)
	for _, rcpt := range []string{"a@clientfirm.test", "b@clientfirm.test"} {
		if r := find(res, rcpt); r.Outcome != DeliveredRemote || r.Match != MatchRecipientSet {
			t.Fatalf("%s: delegated sender (Sender matches log) should resolve, got %s (match=%s)", rcpt, r.Outcome, r.Match)
		}
	}
	// Neither From nor Sender matches the logged sender -> not_found.
	eMiss := eml.Email{From: "X <x@example.test>", Sender: "Y <y@example.test>", To: []string{"a@clientfirm.test", "b@clientfirm.test"}, Date: date}
	for _, r := range Analyze(eMiss, log).Recipients {
		if r.Outcome != NotFound {
			t.Fatalf("neither From nor Sender matching logged sender must stay not_found, got %s", r.Outcome)
		}
	}
}

// WO-42 rev-5 (Finding 1, KLMS): a KLMS scanner line supplies the message-id's
// sender + full recipient set (identification), while queue-id delivery lines
// supply the per-recipient outcomes. A forward with a non-matching Message-ID but
// matching sender/date/set resolves every recipient via recipient_set, cited from
// delivery lines only.
func TestRecipientSetMatchKLMSIdentifiesSet(t *testing.T) {
	log := parseLog(t, `Jun 19 15:00:00 mail01 KLMS: clean: message-id="<wire@example.test>": mail-from="sender@example.test": rcpt-to="a@clientfirm.test","b@clientfirm.test"
Jun 19 15:00:01 mail01 postfix/cleanup[900]: A1C0CC19: message-id=<wire@example.test>
Jun 19 15:00:02 mail01 postfix/smtp[901]: A1C0CC19: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 klms a)
Jun 19 15:00:02 mail01 postfix/smtp[901]: A1C0CC19: to=<b@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.9]:25, status=sent (250 klms b)
`)
	date, _ := time.Parse(time.RFC1123Z, "Fri, 19 Jun 2026 15:00:30 +0000")
	e := eml.Email{From: "Sender <sender@example.test>", To: []string{"a@clientfirm.test", "b@clientfirm.test"}, Date: date}
	res := Analyze(e, log)
	for _, rcpt := range []string{"a@clientfirm.test", "b@clientfirm.test"} {
		r := find(res, rcpt)
		if r.Outcome != DeliveredRemote || r.Match != MatchRecipientSet {
			t.Fatalf("%s: KLMS-identified set should resolve via recipient_set, got %s (match=%s)", rcpt, r.Outcome, r.Match)
		}
		if !strings.Contains(r.Citation, "postfix/smtp") {
			t.Fatalf("%s: citation must come from a delivery line, got %q", rcpt, r.Citation)
		}
	}
}
