package cli

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const filterLog = `Jun  5 15:09:02 mail01 postfix/cleanup[20420]: ABCDEF1: message-id=<sent-1@acme.test>
Jun  5 15:09:20 mail01 postfix/smtp[20440]: ABCDEF1: to=<client@example.test>, relay=mx.example.test[203.0.113.25]:25, status=sent (250 2.0.0 OK: queued as SENT1 подтверждено)
Jun  5 15:09:03 mail01 postfix/cleanup[20421]: ABCDEF2: message-id=<unrelated@acme.test>
Jun  5 15:09:21 mail01 postfix/smtp[20441]: ABCDEF2: to=<ghost@example.test>, relay=mx.example.test[203.0.113.25]:25, status=sent (250 2.0.0 OK: queued as UNRELATED)
`

const filterDovecotSieveStoredLog = `Jul  2 08:12:00 mail01 postfix/cleanup[4000]: STORE1: message-id=<sieve-store@example.test>
Jul  2 08:12:01 mail01 dovecot: lda(clerk)<4050777><6SQCDm4XKGpZzz0ASWwcBg>: sieve: msgid=<sieve-store@example.test>: stored mail into mailbox 'INBOX'
`

const filterOutlookRecipientLog = `Jun 15 09:00:00 mail01 postfix/cleanup[3000]: ABCD01: message-id=<outlook-delivered@example.test>
Jun 15 09:00:01 mail01 postfix/smtp[3001]: ABCD01: to=<alpha@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.11]:25, status=sent (250 OK alpha)
Jun 15 09:00:02 mail01 postfix/smtp[3001]: ABCD01: to=<beta@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.12]:25, status=sent (250 OK beta)
Jun 15 09:00:03 mail01 postfix/smtp[3001]: ABCD01: to=<gamma@example.test>, relay=mx.example.test[203.0.113.13]:25, status=sent (250 OK gamma)
`

const filterEqualsHexRecipientLog = `Jun 15 09:00:00 mail01 postfix/cleanup[3010]: ABCD10: message-id=<equals-hex@example.test>
Jun 15 09:00:01 mail01 postfix/smtp[3011]: ABCD10: to=<case=40example@example.test>, relay=mx.example.test[203.0.113.14]:25, status=sent (250 OK equals)
`

const filterStructuralEqualsRecipientLog = `Jun 15 09:00:00 mail01 postfix/cleanup[3015]: ABCD15: message-id=<structural-equals@example.test>
Jun 15 09:00:01 mail01 postfix/smtp[3016]: ABCD15: to=<case=3dexample@example.test>, relay=mx.example.test[203.0.113.16]:25, status=sent (250 OK structural equals)
`

const filterPrefixEqualsRecipientLog = `Jun 15 09:00:00 mail01 postfix/cleanup[3016]: ABCD16: message-id=<prefix-equals@example.test>
Jun 15 09:00:01 mail01 postfix/smtp[3017]: ABCD16: to=<=3dcase@example.test>, relay=mx.example.test[203.0.113.17]:25, status=sent (250 OK prefix equals)
`

const filterQPAngleRecipientLog = `Jun 15 09:00:00 mail01 postfix/cleanup[3020]: ABCD20: message-id=<qp-angle@example.test>
Jun 15 09:00:01 mail01 postfix/smtp[3021]: ABCD20: to=<john@example.test>, relay=mx.example.test[203.0.113.15]:25, status=sent (250 OK qp angle)
`

const filterMidSoftWrapRecipientLog = `Jun 15 09:00:00 mail01 postfix/cleanup[3030]: ABCD30: message-id=<mid-softwrap@example.test>
Jun 15 09:00:01 mail01 postfix/smtp[3031]: ABCD30: to=<a@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.18]:25, status=sent (250 OK mid)
`

// WO-41: one delivery to the recipient in the window (single queue-id) — the
// unique-match case the Outlook stripped-Message-ID fallback must resolve.
// Queue-ids are hex ([0-9A-F]{6,}), matching real Postfix.
const filterStrippedMsgIDUniqueLog = `Jun 19 15:49:30 mail01 postfix/smtp[5001]: AAAA1119: to=<r@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.40]:25, status=sent (250 OK unique)
`

// WO-41: two DIFFERENT messages (two distinct queue-ids) to the same recipient
// in the window — the ambiguous case that must stay NOT_FOUND rather than guess.
const filterStrippedMsgIDAmbiguousLog = `Jun 19 15:40:00 mail01 postfix/smtp[5010]: BBBB2219: to=<r@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.41]:25, status=sent (250 OK first)
Jun 19 16:10:00 mail01 postfix/smtp[5011]: CCCC3319: to=<r@clientfirm.test>, relay=mx.clientfirm.test[203.0.113.42]:25, status=sent (250 OK second)
`

// sentMailNoMessageID mimics an Outlook forward-as-attachment: the original sent
// message keeps its Date but has NO Message-ID header (Outlook strips it).
func sentMailNoMessageID(recipient, date string) string {
	return `From: Sender <sender@example.test>
To: ` + recipient + `
Subject: Filing
Date: ` + date + `

body
`
}

const filterConfig = `log_year: 2026
receipt_filter:
  domains: [acme.test]
  reply_from: receipt@acme.test
  teams:
    docketing:
      members: [docketing@acme.test, attorney1@acme.test]
`

func TestFilterHappyPathWritesReplyEmail(t *testing.T) {
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig)
	if !strings.Contains(out, "Auto-Submitted: auto-generated") {
		t.Fatalf("reply must carry loop-prevention header, got:\n%s", out)
	}
	msg, err := mail.ReadMessage(strings.NewReader(out))
	if err != nil {
		t.Fatalf("reply should be a parseable message: %v\n%s", err, out)
	}
	if got := msg.Header.Get("From"); got != "receipt@acme.test" {
		t.Fatalf("reply From: got %q", got)
	}
	if !strings.Contains(out, "To: docketing@acme.test") {
		t.Fatalf("reply should go to authenticated envelope sender, got:\n%s", out)
	}
	if _, err := mail.ParseDate(msg.Header.Get("Date")); err != nil {
		t.Fatalf("reply Date should be RFC5322-parseable, got %q: %v", msg.Header.Get("Date"), err)
	}
	_, _, textBody := filterTextPart(t, out)
	if !strings.Contains(textBody, "status=sent") || !strings.Contains(filterDecodedJSON(t, out), `"outcome": "delivered_remote"`) {
		t.Fatalf("happy path should include cited delivered outcome, got:\n%s", out)
	}
}

// WO-19: guard the JSON attachment transfer body, not only the full reply text.
func TestFilterJSONAttachmentBase64EncodesUTF8(t *testing.T) {
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig)
	cte, raw, decoded := filterJSONAttachment(t, out)
	if cte != "base64" {
		t.Fatalf("JSON attachment transfer encoding: got %q", cte)
	}
	if strings.Contains(string(raw), "подтверждено") {
		t.Fatalf("JSON attachment should not contain raw UTF-8 when base64 encoded, got %q", raw)
	}
	if !strings.Contains(string(decoded), "подтверждено") {
		t.Fatalf("decoded JSON attachment should preserve UTF-8 evidence, got:\n%s", decoded)
	}
}

func TestFilterTextPartIsQuotedPrintablePlainReceipt(t *testing.T) {
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig)
	cte, raw, decoded := filterTextPart(t, out)
	if cte != "quoted-printable" {
		t.Fatalf("text part transfer encoding: got %q", cte)
	}
	if strings.Contains(string(raw), "подтверждено") {
		t.Fatalf("text part should not contain raw UTF-8 when quoted-printable encoded, got %q", raw)
	}
	for _, want := range []string{
		"MAIL DELIVERY RECEIPT",
		"MESSAGE",
		"  Subject: Filing",
		"  Overall: DELIVERED - accepted by the remote mail server",
		"RECIPIENTS",
		"  Recipient: client@example.test",
		"  Outcome: DELIVERED",
		"EVIDENCE",
		"status=sent (250 2.0.0 OK: queued as SENT1 подтверждено)",
		"LIMITATION",
	} {
		if !strings.Contains(decoded, want) {
			t.Fatalf("decoded text receipt missing %q:\n%s", want, decoded)
		}
	}
	for _, forbidden := range []string{"# ", "**", "| Recipient |", "```", "✅", "⛔", "⏳", "❓"} {
		if strings.Contains(decoded, forbidden) {
			t.Fatalf("decoded text receipt must not require Markdown/glyph rendering; found %q:\n%s", forbidden, decoded)
		}
	}
}

// WO-40: check must report Dovecot sieve mailbox stores as delivered_local.
func TestCheckDovecotSieveStoredMailIntoMailboxDelivery(t *testing.T) {
	sent := sentMailWithHeaders("sieve-store@example.test", "clerk@example.test",
		"From: Sender <sender@example.test>")
	out := runCheckJSONWithLog(t, sent, filterDovecotSieveStoredLog)
	for _, want := range []string{
		`"outcome": "delivered_local"`,
		`"match_method": "message_id"`,
		`"relay": "dovecot"`,
		`"response": "stored mail into mailbox INBOX"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("check output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"outcome": "not_found"`) {
		t.Fatalf("check must not report Dovecot sieve store as not_found:\n%s", out)
	}
}

// WO-40: filter uses the same analysis path after extracting the forwarded email.
func TestFilterDovecotSieveStoredMailIntoMailboxDelivery(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test, clerk@example.test]
`
	sent := sentMailWithHeaders("sieve-store@example.test", "clerk@example.test",
		"From: Sender <sender@example.test>")
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterDovecotSieveStoredLog)
	decodedJSON := filterDecodedJSON(t, out)
	for _, want := range []string{
		`"outcome": "delivered_local"`,
		`"response": "stored mail into mailbox INBOX"`,
	} {
		if !strings.Contains(decodedJSON, want) {
			t.Fatalf("filter JSON missing %q:\n%s", want, decodedJSON)
		}
	}
	_, _, textBody := filterTextPart(t, out)
	for _, want := range []string{
		"  Outcome: DELIVERED_LOCAL",
		"stored mail into mailbox 'INBOX'",
	} {
		if !strings.Contains(textBody, want) {
			t.Fatalf("filter text missing %q:\n%s", want, textBody)
		}
	}
}

// WO-25: decoded forwarded-message subjects feed both the plain body and reply header.
func TestFilterDecodesRFC2047SubjectInReply(t *testing.T) {
	sent := sentMailWithSubject("sent-1@acme.test", "client@example.test", "=?koi8-r?B?8NLJ18XU?=")
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sent), filterConfig)
	_, _, textBody := filterTextPart(t, out)
	if !strings.Contains(textBody, "  Subject: Привет") {
		t.Fatalf("reply text should include decoded subject, got:\n%s", textBody)
	}
	msg, err := mail.ReadMessage(strings.NewReader(out))
	if err != nil {
		t.Fatalf("reply should be a parseable message: %v\n%s", err, out)
	}
	var subjectDecoder mime.WordDecoder
	subject, err := subjectDecoder.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("reply subject should decode: %v", err)
	}
	if subject != "Mail delivery receipt: Привет" {
		t.Fatalf("reply subject: want %q, got %q", "Mail delivery receipt: Привет", subject)
	}
}

// WO-22: generated reply boundaries replace the legacy static delimiter.
func TestFilterUsesGeneratedBoundaryAndIgnoresLegacySubjectBoundary(t *testing.T) {
	boundary := filterReplyBoundaryPrefix + "00112233445566778899aabbccddeeff"
	withFilterBoundaries(t, boundary)
	sent := sentMailWithSubject("sent-1@acme.test", "client@example.test", "--"+filterLegacyReplyBoundary)
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sent), filterConfig)
	if got := filterReplyBoundaryParam(t, out); got != boundary {
		t.Fatalf("reply boundary: got %q, want %q", got, boundary)
	}
	if strings.Contains(out, `boundary="`+filterLegacyReplyBoundary+`"`) {
		t.Fatalf("reply should not use legacy static boundary, got:\n%s", out)
	}
	if parts := filterReplyPartCount(t, out); parts != 2 {
		t.Fatalf("legacy boundary text in Subject should not create extra parts, got %d parts:\n%s", parts, out)
	}
}

// WO-22: each reply asks the injected boundary source for a fresh value.
func TestFilterBoundarySourceRunsPerReply(t *testing.T) {
	first := filterReplyBoundaryPrefix + "11111111111111111111111111111111"
	second := filterReplyBoundaryPrefix + "22222222222222222222222222222222"
	withFilterBoundaries(t, first, second)
	out1 := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig)
	out2 := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig)
	if got := filterReplyBoundaryParam(t, out1); got != first {
		t.Fatalf("first reply boundary: got %q, want %q", got, first)
	}
	if got := filterReplyBoundaryParam(t, out2); got != second {
		t.Fatalf("second reply boundary: got %q, want %q", got, second)
	}
}

// WO-22: delimiter-line collisions regenerate instead of trusting the first candidate.
func TestFilterBoundaryRegeneratesOnDelimiterLineCollision(t *testing.T) {
	colliding := filterReplyBoundaryPrefix + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	safe := filterReplyBoundaryPrefix + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	withFilterBoundaries(t, colliding, safe)
	got, err := filterReplyBoundary("before\r\n--" + colliding + "\r\nafter")
	if err != nil {
		t.Fatalf("generate boundary: %v", err)
	}
	if got != safe {
		t.Fatalf("boundary after collision: got %q, want %q", got, safe)
	}
}

func TestFilterDropsExternalEnvelopeSender(t *testing.T) {
	out := runFilter(t, "client@example.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig)
	if out != "" {
		t.Fatalf("external sender should be silently dropped, got:\n%s", out)
	}
}

func TestFilterDropsTeamMismatch(t *testing.T) {
	cfg := `receipt_filter:
  domains: [acme.test]
  teams:
    docketing:
      members: [docketing@acme.test, assistant1@acme.test]
    prosecution:
      members: [attorney1@acme.test]
`
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), cfg)
	if out != "" {
		t.Fatalf("team mismatch should be silently dropped, got:\n%s", out)
	}
}

func TestFilterUnknownAttachmentMessageIDReturnsNotFoundOnly(t *testing.T) {
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("missing@acme.test", "ghost@example.test")), filterConfig)
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "not_found"`) {
		t.Fatalf("unknown attachment message-id should produce not_found, got:\n%s", out)
	}
	if strings.Contains(out, "UNRELATED") || strings.Contains(out, "status=sent") {
		t.Fatalf("unknown attachment message-id must not cite unrelated nearby log lines, got:\n%s", out)
	}
}

// WO-36: accepted triggers with recipients but no matching delivery still emit a receipt.
func TestFilterUnknownAttachmentIncludesSearchedRange(t *testing.T) {
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("missing@acme.test", "ghost@example.test")), filterConfig)
	_, _, textBody := filterTextPart(t, out)
	if !strings.Contains(textBody, "Outcome: NOT FOUND") {
		t.Fatalf("missing delivery should emit a not_found receipt, got:\n%s", textBody)
	}
	if !strings.Contains(textBody, "Searched log time range:") {
		t.Fatalf("not_found receipt should state searched range, got:\n%s", textBody)
	}
}

func TestFilterUnknownAttachmentIncludesFullSearchedCoverage(t *testing.T) {
	const coverageLog = `2026-06-01T10:00:00+02:00 mail KLMS: clean: message-id="<coverage-start@example.test>": action="Skipped"
2026-06-05T15:09:02+02:00 mail postfix/cleanup[20420]: ABCDEF1: message-id=<sent-1@acme.test>
2026-06-05T15:09:20+02:00 mail postfix/smtp[20440]: ABCDEF1: to=<client@example.test>, relay=mx.example.test[203.0.113.25]:25, status=sent (250 OK)
2026-06-20T18:30:00+02:00 mail postfix/qmgr[20499]: idle
`
	out := runFilterWithLogAndArgs(t, "docketing@acme.test",
		triggerWithAttachment(sentMail("missing@acme.test", "ghost@example.test")), filterConfig, coverageLog)
	_, _, textBody := filterTextPart(t, out)
	if !strings.Contains(textBody, "Searched log time range: 2026-06-01 10:00:00 +0200 to 2026-06-20 18:30:00 +0200.") {
		t.Fatalf("not_found receipt should report full searched coverage, got:\n%s", textBody)
	}
}

func TestFilterUnknownAttachmentReportsCoverageWithoutDeliveryLines(t *testing.T) {
	const scannerOnlyLog = `2026-06-01T10:00:00+02:00 mail KLMS: clean: message-id="<scanner-only@example.test>": action="Skipped"
2026-06-01T10:30:00+02:00 mail postfix/qmgr[20499]: idle
`
	out := runFilterWithLogAndArgs(t, "docketing@acme.test",
		triggerWithAttachment(sentMail("missing@acme.test", "ghost@example.test")), filterConfig, scannerOnlyLog)
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "not_found"`) {
		t.Fatalf("scanner-only log should still produce a not_found receipt, got:\n%s", out)
	}
	_, _, textBody := filterTextPart(t, out)
	if !strings.Contains(textBody, "Searched log time range: 2026-06-01 10:00:00 +0200 to 2026-06-01 10:30:00 +0200.") {
		t.Fatalf("not_found receipt should report timestamp coverage without delivery events, got:\n%s", textBody)
	}
}

// WO-36: no attachment and no parseable forwarded recipients must be visible.
func TestFilterNoForwardedMessageWritesAdmittedReason(t *testing.T) {
	trigger := `From: Sender <sender@example.test>
To: receipt@example.test
Subject: receipt request

Please receipt this.
`
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
`
	out, errOut := runFilterResultWithLogAndArgs(t, "sender@example.test", trigger, cfg, filterLog)
	if out != "" {
		t.Fatalf("unparseable trigger should not emit a reply, got:\n%s", out)
	}
	if !strings.Contains(errOut, "no message/rfc822 attachment or parseable forwarded recipients found") {
		t.Fatalf("stderr should name missing forwarded message, got %q", errOut)
	}
}

// WO-36: an attached message with no recipients is an admitted refusal, not silence.
func TestFilterAttachmentWithNoRecipientsWritesAdmittedReason(t *testing.T) {
	attached := `From: Sender <sender@example.test>
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <no-recipient@example.test>

body
`
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
`
	out, errOut := runFilterResultWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(attached), cfg, filterLog)
	if out != "" {
		t.Fatalf("recipientless attachment should not emit a reply, got:\n%s", out)
	}
	if !strings.Contains(errOut, "forwarded message attachment has no parsed recipients") {
		t.Fatalf("stderr should name zero parsed recipients, got %q", errOut)
	}
}

// WO-37: Outlook-mangled recipients recovered from the attachment feed delivery lookup.
func TestFilterOutlookMailtoDoubledRecipientsAreDelivered(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := "From: Sender <sender@example.test>\r\n" +
		"To: 'Alpha One' < <mailto:alpha@clientfirm.test> alpha@clientfirm.test>; =\r\n" +
		" 'Beta Two' < <mailto:beta@clientfirm.test> beta@clientfirm.test>\r\n" +
		"Cc: Gamma <gamma@example.test>\r\n" +
		"Subject: Filing\r\n" +
		"Date: Fri, 15 Jun 2026 09:00:00 +0000\r\n" +
		"Message-ID: <outlook-delivered@example.test>\r\n" +
		"\r\n" +
		"body\r\n"
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterOutlookRecipientLog)
	decodedJSON := filterDecodedJSON(t, out)
	for _, want := range []string{"alpha@clientfirm.test", "beta@clientfirm.test", "gamma@example.test"} {
		if !strings.Contains(decodedJSON, `"recipient": "`+want+`"`) {
			t.Fatalf("filter JSON missing recovered recipient %q:\n%s", want, decodedJSON)
		}
	}
	if got := strings.Count(decodedJSON, `"outcome": "delivered_remote"`); got != 3 {
		t.Fatalf("all recovered recipients should be delivered_remote, got %d:\n%s", got, decodedJSON)
	}
}

// WO-37: a mid-address QP soft break must be rejoined so the recipient still
// resolves against the delivery log instead of being dropped.
func TestFilterRecoversMidSoftWrappedRecipient(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := "From: Sender <sender@example.test>\r\n" +
		"To: a@client=\r\nfirm.test\r\n" +
		"Subject: Filing\r\n" +
		"Date: Fri, 15 Jun 2026 09:00:00 +0000\r\n" +
		"Message-ID: <mid-softwrap@example.test>\r\n" +
		"\r\n" +
		"body\r\n"
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterMidSoftWrapRecipientLog)
	decodedJSON := filterDecodedJSON(t, out)
	if !strings.Contains(decodedJSON, `"recipient": "a@clientfirm.test"`) {
		t.Fatalf("filter JSON missing rejoined recipient:\n%s", decodedJSON)
	}
	if !strings.Contains(decodedJSON, `"outcome": "delivered_remote"`) {
		t.Fatalf("rejoined recipient should match the delivery log, got:\n%s", decodedJSON)
	}
}

// WO-41: Outlook forward-as-attachment strips the original Message-ID. With no
// Message-ID but a Date, a UNIQUE recipient+window match must still resolve.
func TestFilterStrippedMessageIDUniqueWindowMatchResolves(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := sentMailNoMessageID("r@clientfirm.test", "Fri, 19 Jun 2026 15:49:35 +0000")
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterStrippedMsgIDUniqueLog)
	decodedJSON := filterDecodedJSON(t, out)
	if !strings.Contains(decodedJSON, `"recipient": "r@clientfirm.test"`) {
		t.Fatalf("filter JSON missing recipient:\n%s", decodedJSON)
	}
	if !strings.Contains(decodedJSON, `"outcome": "delivered_remote"`) {
		t.Fatalf("unique window match should resolve to delivered_remote:\n%s", decodedJSON)
	}
	// WO-42: a unique recipient-set match (recipient_set) or the per-recipient
	// window (recipient_window) are both valid no-Message-ID resolutions.
	if !strings.Contains(decodedJSON, `"match_method": "recipient_set"`) &&
		!strings.Contains(decodedJSON, `"match_method": "recipient_window"`) {
		t.Fatalf("expected a recipient-based match method:\n%s", decodedJSON)
	}
}

// WO-41: when the window catches TWO different messages (two queue-ids) to the
// same recipient, attribution is ambiguous and must stay NOT_FOUND, not guess.
func TestFilterStrippedMessageIDAmbiguousWindowStaysNotFound(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := sentMailNoMessageID("r@clientfirm.test", "Fri, 19 Jun 2026 15:49:35 +0000")
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterStrippedMsgIDAmbiguousLog)
	decodedJSON := filterDecodedJSON(t, out)
	if !strings.Contains(decodedJSON, `"outcome": "not_found"`) {
		t.Fatalf("ambiguous window (two queue-ids) must stay not_found, got:\n%s", decodedJSON)
	}
	if strings.Contains(decodedJSON, `"outcome": "delivered_remote"`) {
		t.Fatalf("must not attribute a delivery on an ambiguous window match:\n%s", decodedJSON)
	}
}

// WO-37: quoted-printable repair must not corrupt valid =HH local-part bytes.
func TestFilterPreservesEqualsHexRecipientLocalPart(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := sentMailWithHeaders("equals-hex@example.test", "case=40example@example.test",
		"From: Sender <sender@example.test>")
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterEqualsHexRecipientLog)
	decodedJSON := filterDecodedJSON(t, out)
	if !strings.Contains(decodedJSON, `"recipient": "case=40example@example.test"`) {
		t.Fatalf("filter JSON missing preserved recipient:\n%s", decodedJSON)
	}
	if strings.Contains(decodedJSON, `"recipient": "example@example.test"`) {
		t.Fatalf("filter must not extract suffix recipient after QP corruption:\n%s", decodedJSON)
	}
	if !strings.Contains(decodedJSON, `"outcome": "delivered_remote"`) {
		t.Fatalf("preserved recipient should match the delivery log, got:\n%s", decodedJSON)
	}
}

// WO-37: structural-looking =HH local-part bytes remain raw fallback tokens.
func TestFilterPreservesStructuralEqualsHexRecipientLocalPart(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := sentMailWithHeaders("structural-equals@example.test", "Case <case=3dexample@example.test> trailing text",
		"From: Sender <sender@example.test>")
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterStructuralEqualsRecipientLog)
	decodedJSON := filterDecodedJSON(t, out)
	if !strings.Contains(decodedJSON, `"recipient": "case=3dexample@example.test"`) {
		t.Fatalf("filter JSON missing preserved structural-looking recipient:\n%s", decodedJSON)
	}
	if strings.Contains(decodedJSON, `"recipient": "case=example@example.test"`) ||
		strings.Contains(decodedJSON, `"recipient": "example@example.test"`) {
		t.Fatalf("filter must not decode or suffix-match structural-looking raw recipient:\n%s", decodedJSON)
	}
	if !strings.Contains(decodedJSON, `"outcome": "delivered_remote"`) {
		t.Fatalf("preserved structural-looking recipient should match the delivery log, got:\n%s", decodedJSON)
	}
}

// WO-37: prefix =HH local-part bytes must survive parser fallback and log lookup.
func TestFilterPreservesPrefixEqualsHexRecipientLocalPart(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := sentMailWithHeaders("prefix-equals@example.test", "<=3dcase@example.test> trailing text",
		"From: Sender <sender@example.test>")
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterPrefixEqualsRecipientLog)
	decodedJSON := filterDecodedJSON(t, out)
	if !strings.Contains(decodedJSON, `"recipient": "=3dcase@example.test"`) {
		t.Fatalf("filter JSON missing preserved prefix recipient:\n%s", decodedJSON)
	}
	if strings.Contains(decodedJSON, `"recipient": "=case@example.test"`) ||
		strings.Contains(decodedJSON, `"recipient": "case@example.test"`) {
		t.Fatalf("filter must not decode or suffix-match prefix raw recipient:\n%s", decodedJSON)
	}
	if !strings.Contains(decodedJSON, `"outcome": "delivered_remote"`) {
		t.Fatalf("preserved prefix recipient should match the delivery log, got:\n%s", decodedJSON)
	}
}

// WO-37: QP-decoded angle delimiters must feed delivery lookup before fallback.
func TestFilterQuotedPrintableAngleRecipientIsDelivered(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := sentMailWithHeaders("qp-angle@example.test", "=3Cjohn@example.test=3E",
		"From: Sender <sender@example.test>")
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterQPAngleRecipientLog)
	decodedJSON := filterDecodedJSON(t, out)
	if !strings.Contains(decodedJSON, `"recipient": "john@example.test"`) {
		t.Fatalf("filter JSON missing decoded recipient:\n%s", decodedJSON)
	}
	if strings.Contains(decodedJSON, `"recipient": "3cjohn@example.test"`) ||
		strings.Contains(decodedJSON, `"recipient": "=3cjohn@example.test"`) {
		t.Fatalf("filter must not use regex fallback before QP decode:\n%s", decodedJSON)
	}
	if !strings.Contains(decodedJSON, `"outcome": "delivered_remote"`) {
		t.Fatalf("decoded recipient should match the delivery log, got:\n%s", decodedJSON)
	}
}

// WO-37: nested QP angle delimiters must decode before raw-token fallback so
// the delivered recipient, not a literal =3C-prefixed artifact, is correlated.
func TestFilterNestedQuotedPrintableAngleRecipientIsDelivered(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := sentMailWithHeaders("qp-angle@example.test", "<=3Cjohn@example.test=3E> trailing text",
		"From: Sender <sender@example.test>")
	out := runFilterWithLogAndArgs(t, "sender@example.test", triggerWithAttachment(sent), cfg, filterQPAngleRecipientLog)
	decodedJSON := filterDecodedJSON(t, out)
	if !strings.Contains(decodedJSON, `"recipient": "john@example.test"`) {
		t.Fatalf("filter JSON missing decoded nested recipient:\n%s", decodedJSON)
	}
	if strings.Contains(decodedJSON, `"recipient": "3cjohn@example.test"`) ||
		strings.Contains(decodedJSON, `"recipient": "=3cjohn@example.test"`) {
		t.Fatalf("filter must not use raw nested delimiter artifact:\n%s", decodedJSON)
	}
	if !strings.Contains(decodedJSON, `"outcome": "delivered_remote"`) {
		t.Fatalf("decoded nested recipient should match the delivery log, got:\n%s", decodedJSON)
	}
}

// WO-38: filter can search a rotated gzip log via glob, not only current mail.log.
func TestFilterSearchesRotatedGzipLogGlob(t *testing.T) {
	cfg := `receipt_filter:
  domains: [example.test]
  reply_from: receipt@example.test
  teams:
    legal:
      members: [sender@example.test]
`
	sent := sentMailWithHeaders("outlook-delivered@example.test", "alpha@clientfirm.test",
		"From: Sender <sender@example.test>")
	out, errOut := runFilterResultWithPreparedLog(t, "sender@example.test", triggerWithAttachment(sent), cfg, "mail.log*", func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "mail.log"), []byte("Jun 20 10:00:00 mail01 postfix/qmgr[1]: idle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeGzipFile(t, filepath.Join(dir, "mail.log.2.gz"), filterOutlookRecipientLog)
	})
	if errOut != "" {
		t.Fatalf("filter should not write stderr for rotated-log success, got %q", errOut)
	}
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "delivered_remote"`) {
		t.Fatalf("rotated gzip delivery should be found, got:\n%s", out)
	}
}

func TestFilterInlineForwardWithoutTimeStaysNotFound(t *testing.T) {
	inline := `From: Docketing <docketing@acme.test>
To: receipt@acme.test
Subject: receipt request

From: Attorney <attorney1@acme.test>
Sent: not a date
To: ghost@example.test
Subject: Filing

body
`
	out := runFilter(t, "docketing@acme.test", inline, filterConfig)
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "not_found"`) {
		t.Fatalf("unbounded inline fallback should be not_found, got:\n%s", out)
	}
	if strings.Contains(out, "UNRELATED") || strings.Contains(out, "status=sent") {
		t.Fatalf("unbounded inline fallback must not cite unrelated log lines, got:\n%s", out)
	}
}

func TestFilterLoopGuardDropsAutoSubmitted(t *testing.T) {
	trigger := "Auto-Submitted: auto-generated\n" + triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test"))
	out := runFilter(t, "docketing@acme.test", trigger, filterConfig)
	if out != "" {
		t.Fatalf("auto-submitted trigger should be silently dropped, got:\n%s", out)
	}
}

func TestFilterAuthorizesWhenFromSharesTeamEvenIfSenderDoesNot(t *testing.T) {
	sent := sentMailWithHeaders("sent-1@acme.test", "client@example.test",
		"From: Attorney <attorney1@acme.test>\nSender: Other <other@acme.test>")
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sent), filterConfig)
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "delivered_remote"`) {
		t.Fatalf("From sharing team should authorize even when Sender does not, got:\n%s", out)
	}
}

func TestFilterAuthorizesWhenSenderSharesTeam(t *testing.T) {
	sent := sentMailWithHeaders("sent-1@acme.test", "client@example.test",
		"From: Other <other@acme.test>\nSender: Attorney <attorney1@acme.test>")
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sent), filterConfig)
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "delivered_remote"`) {
		t.Fatalf("Sender sharing team should authorize, got:\n%s", out)
	}
}

func TestFilterDropsWhenNeitherFromNorSenderSharesTeam(t *testing.T) {
	sent := sentMailWithHeaders("sent-1@acme.test", "client@example.test",
		"From: Other <other@acme.test>\nSender: Outside <outside@acme.test>")
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sent), filterConfig)
	if out != "" {
		t.Fatalf("neither From nor Sender sharing team should drop, got:\n%s", out)
	}
}

func TestFilterUsesWholeDomainWhenNoTeamsConfigured(t *testing.T) {
	cfg := `receipt_filter:
  domains: [acme.test]
`
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), cfg)
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "delivered_remote"`) {
		t.Fatalf("no teams should fall back to whole-domain authorization, got:\n%s", out)
	}
}

// WO-23: missing authenticated envelope senders must fail closed.
func TestFilterDropsEmptyEnvelopeSender(t *testing.T) {
	out := runFilter(t, "", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig)
	if out != "" {
		t.Fatalf("empty envelope sender should be silently dropped, got:\n%s", out)
	}
}

// WO-20: malformed authenticated senders must not pass suffix-domain checks.
func TestFilterDropsMalformedEnvelopeSender(t *testing.T) {
	cfg := `receipt_filter:
  domains: [acme.test]
  reply_from: receipt@acme.test
`
	out := runFilter(t, "attacker@example.test@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), cfg)
	if out != "" {
		t.Fatalf("malformed envelope sender should be silently dropped, got:\n%s", out)
	}
}

// WO-20: explicit reply identities must be single valid mailbox addresses.
func TestFilterDropsMalformedFromFlag(t *testing.T) {
	out := runFilterWithArgs(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig,
		"--from", "receipt@example.test@acme.test")
	if out != "" {
		t.Fatalf("malformed --from should be silently dropped, got:\n%s", out)
	}
}

// WO-21: quoted local-parts are not emitted as raw trusted reply headers.
func TestFilterDropsQuotedLocalPartFromFlag(t *testing.T) {
	out := runFilterWithArgs(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig,
		"--from", `Receipts <"receipt bot"@acme.test>`)
	if out != "" {
		t.Fatalf("quoted-local --from should be silently dropped, got:\n%s", out)
	}
}

// WO-20: configured reply identities use the same strict parser as --from.
func TestFilterDropsMalformedConfigReplyFrom(t *testing.T) {
	cfg := `receipt_filter:
  domains: [acme.test]
  reply_from: receipt@example.test@acme.test
  teams:
    docketing:
      members: [docketing@acme.test, attorney1@acme.test]
`
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), cfg)
	if out != "" {
		t.Fatalf("malformed receipt_filter.reply_from should be silently dropped, got:\n%s", out)
	}
}

// WO-21: config reply_from cannot normalize to an unsafe raw From value.
func TestFilterDropsQuotedLocalPartConfigReplyFrom(t *testing.T) {
	cfg := `receipt_filter:
  domains: [acme.test]
  reply_from: "receipt bot"@acme.test
  teams:
    docketing:
      members: [docketing@acme.test, attorney1@acme.test]
`
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), cfg)
	if out != "" {
		t.Fatalf("quoted-local receipt_filter.reply_from should be silently dropped, got:\n%s", out)
	}
}

// WO-20: valid --from values still override receipt_filter.reply_from.
func TestFilterFromFlagOverridesConfigReplyFrom(t *testing.T) {
	out := runFilterWithArgs(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig,
		"--from", "Receipts <alerts@acme.test>")
	msg, err := mail.ReadMessage(strings.NewReader(out))
	if err != nil {
		t.Fatalf("reply should be a parseable message: %v\n%s", err, out)
	}
	if got := msg.Header.Get("From"); got != "alerts@acme.test" {
		t.Fatalf("--from should override config reply_from, got %q", got)
	}
}

func TestFilterLoopGuardDropsPrecedenceBulk(t *testing.T) {
	trigger := "Precedence: bulk\n" + triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test"))
	out := runFilter(t, "docketing@acme.test", trigger, filterConfig)
	if out != "" {
		t.Fatalf("bulk trigger should be silently dropped, got:\n%s", out)
	}
}

func runFilter(t *testing.T, envelopeFrom, trigger, cfg string) string {
	t.Helper()
	return runFilterWithArgs(t, envelopeFrom, trigger, cfg)
}

func runFilterWithArgs(t *testing.T, envelopeFrom, trigger, cfg string, extraArgs ...string) string {
	t.Helper()
	return runFilterWithLogAndArgs(t, envelopeFrom, trigger, cfg, filterLog, extraArgs...)
}

func runFilterWithLogAndArgs(t *testing.T, envelopeFrom, trigger, cfg, logBody string, extraArgs ...string) string {
	t.Helper()
	out, _ := runFilterResultWithLogAndArgs(t, envelopeFrom, trigger, cfg, logBody, extraArgs...)
	return out
}

func runFilterResultWithLogAndArgs(t *testing.T, envelopeFrom, trigger, cfg, logBody string, extraArgs ...string) (string, string) {
	t.Helper()
	return runFilterResultWithPreparedLog(t, envelopeFrom, trigger, cfg, "mail.log", func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "mail.log"), []byte(logBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}, extraArgs...)
}

func runFilterResultWithPreparedLog(t *testing.T, envelopeFrom, trigger, cfg, logSpec string, prepare func(string), extraArgs ...string) (string, string) {
	t.Helper()
	oldNow := filterNow
	filterNow = func() time.Time {
		return time.Date(2026, 6, 7, 14, 2, 0, 0, time.UTC)
	}
	t.Cleanup(func() { filterNow = oldNow })

	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".mailreceipt.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	prepare(dir)
	cmd := Root()
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(trigger))
	args := []string{"filter", "--envelope-from", envelopeFrom, "--log", logSpec, "--log-year", "2026"}
	args = append(args, extraArgs...)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("filter returned error: %v stderr=%s", err, stderr.String())
	}
	return out.String(), stderr.String()
}

func runCheckJSONWithLog(t *testing.T, message, logBody string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "mail.log")
	if err := os.WriteFile(logPath, []byte(logBody), 0o644); err != nil {
		t.Fatal(err)
	}
	messagePath := filepath.Join(dir, "sent.eml")
	if err := os.WriteFile(messagePath, []byte(message), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := Root()
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"check", messagePath, "--log", logPath, "--log-year", "2026", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("check returned error: %v stderr=%s", err, stderr.String())
	}
	return out.String()
}

// WO-22: inject deterministic boundary values without reading crypto/rand in assertions.
func withFilterBoundaries(t *testing.T, boundaries ...string) {
	t.Helper()
	oldBoundary := filterNewBoundary
	next := 0
	filterNewBoundary = func() (string, error) {
		if next >= len(boundaries) {
			return "", io.ErrUnexpectedEOF
		}
		boundary := boundaries[next]
		next++
		return boundary, nil
	}
	t.Cleanup(func() { filterNewBoundary = oldBoundary })
}

// WO-22: expose the generated MIME boundary for deterministic assertions.
func filterReplyBoundaryParam(t *testing.T, out string) string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(out))
	if err != nil {
		t.Fatalf("reply should be a parseable message: %v\n%s", err, out)
	}
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("reply content type should parse: %v", err)
	}
	return params["boundary"]
}

// WO-22: count parsed MIME parts to catch delimiter injection regressions.
func filterReplyPartCount(t *testing.T, out string) int {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(out))
	if err != nil {
		t.Fatalf("reply should be a parseable message: %v\n%s", err, out)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("reply content type should parse: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("reply content type: got %q", mediaType)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	parts := 0
	for {
		_, err := mr.NextPart()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Fatalf("read reply part: %v", err)
		}
		parts++
	}
}

// WO-19: decode the generated attachment exactly as a receiver would.
func filterJSONAttachment(t *testing.T, out string) (string, []byte, []byte) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(out))
	if err != nil {
		t.Fatalf("reply should be a parseable message: %v\n%s", err, out)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("reply content type should parse: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("reply content type: got %q", mediaType)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read reply part: %v", err)
		}
		if part.FileName() != "mailreceipt.json" {
			continue
		}
		raw, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read JSON attachment: %v", err)
		}
		cte := strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding")))
		if cte != "base64" {
			return cte, raw, raw
		}
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(raw)))
		if err != nil {
			t.Fatalf("decode JSON attachment: %v", err)
		}
		return cte, raw, decoded
	}
	t.Fatal("reply should include mailreceipt.json attachment")
	return "", nil, nil
}

// WO-26: decode the generated text part exactly as a conservative client would.
func filterTextPart(t *testing.T, out string) (string, []byte, string) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(out))
	if err != nil {
		t.Fatalf("reply should be a parseable message: %v\n%s", err, out)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("reply content type should parse: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("reply content type: got %q", mediaType)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := mr.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read reply part: %v", err)
		}
		partMedia, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("text part content type should parse: %v", err)
		}
		if partMedia != "text/plain" {
			continue
		}
		raw, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read text part: %v", err)
		}
		cte := strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding")))
		if cte != "quoted-printable" {
			return cte, raw, string(raw)
		}
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		if err != nil {
			t.Fatalf("decode text part: %v", err)
		}
		return cte, raw, string(decoded)
	}
	t.Fatal("reply should include text/plain part")
	return "", nil, ""
}

// WO-19: existing receipt assertions inspect the decoded JSON attachment.
func filterDecodedJSON(t *testing.T, out string) string {
	t.Helper()
	_, _, decoded := filterJSONAttachment(t, out)
	return string(decoded)
}

func triggerWithAttachment(attached string) string {
	return `From: Docketing <docketing@acme.test>
To: receipt@acme.test
Subject: receipt request
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain

Please receipt this sent mail.
--b
Content-Type: message/rfc822
Content-Disposition: attachment; filename="sent.eml"

` + attached + `
--b--
`
}

func sentMail(messageID, recipient string) string {
	return sentMailWithHeaders(messageID, recipient, "From: Attorney <attorney1@acme.test>")
}

// WO-22: subject-specific sent-mail fixtures carry legacy boundary probes.
func sentMailWithSubject(messageID, recipient, subject string) string {
	return `From: Attorney <attorney1@acme.test>
To: ` + recipient + `
Subject: ` + subject + `
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <` + messageID + `>

body
`
}

func sentMailWithHeaders(messageID, recipient, headers string) string {
	return headers + `
To: ` + recipient + `
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <` + messageID + `>

body
`
}

func writeGzipFile(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// WO-32: a trigger carrying its own Message-ID, so dedup can key on it.
func triggerWithMessageID(mid, attached string) string {
	return `From: Docketing <docketing@acme.test>
To: receipt@acme.test
Subject: receipt request
Message-ID: <` + mid + `>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain

Please receipt this sent mail.
--b
Content-Type: message/rfc822
Content-Disposition: attachment; filename="sent.eml"

` + attached + `
--b--
`
}

func TestFilterDedupSuppressesDuplicateTrigger(t *testing.T) {
	dedup := t.TempDir()
	trigger := triggerWithMessageID("trigger-dup-1@acme.test", sentMail("sent-1@acme.test", "client@example.test"))

	first := runFilterWithArgs(t, "docketing@acme.test", trigger, filterConfig, "--dedup-dir", dedup)
	if !strings.Contains(first, "MAIL DELIVERY RECEIPT") {
		t.Fatalf("first trigger should emit a receipt, got:\n%s", first)
	}
	second := runFilterWithArgs(t, "docketing@acme.test", trigger, filterConfig, "--dedup-dir", dedup)
	if strings.TrimSpace(second) != "" {
		t.Fatalf("re-delivered identical trigger must emit NO receipt, got:\n%s", second)
	}
}

func TestFilterDedupDistinctTriggersBothEmit(t *testing.T) {
	dedup := t.TempDir()
	t1 := triggerWithMessageID("trigger-a@acme.test", sentMail("sent-1@acme.test", "client@example.test"))
	t2 := triggerWithMessageID("trigger-b@acme.test", sentMail("sent-1@acme.test", "client@example.test"))

	if out := runFilterWithArgs(t, "docketing@acme.test", t1, filterConfig, "--dedup-dir", dedup); !strings.Contains(out, "MAIL DELIVERY RECEIPT") {
		t.Fatalf("trigger A should emit")
	}
	if out := runFilterWithArgs(t, "docketing@acme.test", t2, filterConfig, "--dedup-dir", dedup); !strings.Contains(out, "MAIL DELIVERY RECEIPT") {
		t.Fatalf("distinct trigger B must still emit (different Message-ID)")
	}
}

func TestFilterDedupOffByDefaultEmitsEachTime(t *testing.T) {
	trigger := triggerWithMessageID("trigger-nodd@acme.test", sentMail("sent-1@acme.test", "client@example.test"))
	// No --dedup-dir: behavior unchanged, both invocations emit.
	if out := runFilter(t, "docketing@acme.test", trigger, filterConfig); !strings.Contains(out, "MAIL DELIVERY RECEIPT") {
		t.Fatalf("first emit")
	}
	if out := runFilter(t, "docketing@acme.test", trigger, filterConfig); !strings.Contains(out, "MAIL DELIVERY RECEIPT") {
		t.Fatalf("without dedup, second invocation must still emit")
	}
}
