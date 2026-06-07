package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const filterLog = `Jun  5 15:09:02 mail01 postfix/cleanup[20420]: ABCDEF1: message-id=<sent-1@acme.test>
Jun  5 15:09:20 mail01 postfix/smtp[20440]: ABCDEF1: to=<client@example.test>, relay=mx.example.test[203.0.113.25]:25, status=sent (250 2.0.0 OK: queued as SENT1)
Jun  5 15:09:03 mail01 postfix/cleanup[20421]: ABCDEF2: message-id=<unrelated@acme.test>
Jun  5 15:09:21 mail01 postfix/smtp[20441]: ABCDEF2: to=<ghost@example.test>, relay=mx.example.test[203.0.113.25]:25, status=sent (250 2.0.0 OK: queued as UNRELATED)
`

const filterConfig = `log_year: 2026
receipt_filter:
  domains: [acme.test]
  teams:
    docketing:
      members: [docketing@acme.test, attorney1@acme.test]
`

func TestFilterHappyPathWritesReplyEmail(t *testing.T) {
	out := runFilter(t, "docketing@acme.test", triggerWithAttachment(sentMail("sent-1@acme.test", "client@example.test")), filterConfig)
	if !strings.Contains(out, "Auto-Submitted: auto-generated") {
		t.Fatalf("reply must carry loop-prevention header, got:\n%s", out)
	}
	if !strings.Contains(out, "To: docketing@acme.test") {
		t.Fatalf("reply should go to authenticated envelope sender, got:\n%s", out)
	}
	if !strings.Contains(out, "status=sent") || !strings.Contains(out, `"outcome": "delivered"`) {
		t.Fatalf("happy path should include cited delivered outcome, got:\n%s", out)
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
	if !strings.Contains(out, `"outcome": "not_found"`) {
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
	if !strings.Contains(out, `"outcome": "not_found"`) {
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

func runFilter(t *testing.T, envelopeFrom, trigger, cfg string) string {
	t.Helper()
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
	cmd.SetArgs([]string{"filter", "--envelope-from", envelopeFrom, "--log", logPath, "--log-year", "2026"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("filter returned error: %v stderr=%s", err, stderr.String())
	}
	return out.String()
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
	return `From: Attorney <attorney1@acme.test>
To: ` + recipient + `
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <` + messageID + `>

body
`
}
