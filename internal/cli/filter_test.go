package cli

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
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
	if !strings.Contains(out, "status=sent") || !strings.Contains(filterDecodedJSON(t, out), `"outcome": "delivered"`) {
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
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "delivered"`) {
		t.Fatalf("From sharing team should authorize even when Sender does not, got:\n%s", out)
	}
}

func TestFilterAuthorizesWhenSenderSharesTeam(t *testing.T) {
	sent := sentMailWithHeaders("sent-1@acme.test", "client@example.test",
		"From: Other <other@acme.test>\nSender: Attorney <attorney1@acme.test>")
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sent), filterConfig)
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "delivered"`) {
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
	if !strings.Contains(filterDecodedJSON(t, out), `"outcome": "delivered"`) {
		t.Fatalf("no teams should fall back to whole-domain authorization, got:\n%s", out)
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
	oldNow := filterNow
	filterNow = func() time.Time {
		return time.Date(2026, 6, 7, 14, 2, 0, 0, time.UTC)
	}
	t.Cleanup(func() { filterNow = oldNow })

	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "mail.log")
	if err := os.WriteFile(logPath, []byte(filterLog), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mailreceipt.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := Root()
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(trigger))
	args := []string{"filter", "--envelope-from", envelopeFrom, "--log", logPath, "--log-year", "2026"}
	args = append(args, extraArgs...)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("filter returned error: %v stderr=%s", err, stderr.String())
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
