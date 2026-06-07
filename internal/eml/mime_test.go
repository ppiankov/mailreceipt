package eml

import (
	"bytes"
	"encoding/base64"
	"mime/quotedprintable"
	"strings"
	"testing"
)

func TestExtractForwardedMessageRFC822Attachment(t *testing.T) {
	raw := `From: Docketing <docketing@acme.test>
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

From: Attorney <attorney1@acme.test>
To: client@example.test
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <sent-1@acme.test>

body
--b--
`
	fwd, err := ExtractForwardedEmail([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !fwd.Attached {
		t.Fatal("message/rfc822 part should be marked as an attachment selector")
	}
	if fwd.Email.MessageID != "sent-1@acme.test" {
		t.Fatalf("message-id: got %q", fwd.Email.MessageID)
	}
	if got := fwd.Email.Recipients(); len(got) != 1 || got[0] != "client@example.test" {
		t.Fatalf("recipients: got %v", got)
	}
}

func TestExtractForwardedInlineFallback(t *testing.T) {
	raw := `From: Docketing <docketing@acme.test>
To: receipt@acme.test
Subject: receipt request

From: Attorney <attorney1@acme.test>
Sent: Friday, June 5, 2026 3:09 PM
To: client@example.test
Subject: Filing

body
`
	fwd, err := ExtractForwardedEmail([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.Attached {
		t.Fatal("plain inline forward must not be marked as an attachment selector")
	}
	if !strings.Contains(strings.ToLower(fwd.Email.From), "attorney1@acme.test") {
		t.Fatalf("inline from: got %q", fwd.Email.From)
	}
	if got := fwd.Email.Recipients(); len(got) != 1 || got[0] != "client@example.test" {
		t.Fatalf("inline recipients: got %v", got)
	}
}

func TestExtractForwardedBase64MessageRFC822Attachment(t *testing.T) {
	raw := triggerWithForwardedPart(
		"message/rfc822",
		`attachment; filename="sent.eml"`,
		"base64",
		base64.StdEncoding.EncodeToString([]byte(sentMessage("sent-base64@acme.test"))),
	)
	fwd, err := ExtractForwardedEmail([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !fwd.Attached {
		t.Fatal("base64 message/rfc822 part should be marked as an attachment selector")
	}
	if fwd.Email.MessageID != "sent-base64@acme.test" {
		t.Fatalf("message-id: got %q", fwd.Email.MessageID)
	}
}

func TestExtractForwardedQuotedPrintableMessageRFC822Attachment(t *testing.T) {
	var encoded bytes.Buffer
	qp := quotedprintable.NewWriter(&encoded)
	if _, err := qp.Write([]byte(sentMessage("sent-qp@acme.test"))); err != nil {
		t.Fatal(err)
	}
	if err := qp.Close(); err != nil {
		t.Fatal(err)
	}

	raw := triggerWithForwardedPart(
		"message/rfc822",
		`attachment; filename="sent.eml"`,
		"quoted-printable",
		encoded.String(),
	)
	fwd, err := ExtractForwardedEmail([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !fwd.Attached {
		t.Fatal("quoted-printable message/rfc822 part should be marked as an attachment selector")
	}
	if fwd.Email.MessageID != "sent-qp@acme.test" {
		t.Fatalf("message-id: got %q", fwd.Email.MessageID)
	}
}

func TestExtractForwardedGenericEMLAttachmentByFilename(t *testing.T) {
	raw := triggerWithForwardedPart(
		"application/octet-stream",
		`attachment; filename="sent.eml"`,
		"",
		sentMessage("sent-generic@acme.test"),
	)
	fwd, err := ExtractForwardedEmail([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !fwd.Attached {
		t.Fatal("generic .eml attachment should be marked as an attachment selector")
	}
	if fwd.Email.MessageID != "sent-generic@acme.test" {
		t.Fatalf("message-id: got %q", fwd.Email.MessageID)
	}
}

func TestExtractForwardedPrefersAttachmentOverText(t *testing.T) {
	raw := strings.Replace(triggerWithForwardedPart(
		"message/rfc822",
		`attachment; filename="sent.eml"`,
		"",
		sentMessage("sent-attached@acme.test"),
	), "Please receipt this sent mail.", sentMessage("sent-inline@acme.test"), 1)

	fwd, err := ExtractForwardedEmail([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.Email.MessageID != "sent-attached@acme.test" {
		t.Fatalf("should prefer attachment over text fallback, got %q", fwd.Email.MessageID)
	}
}

func TestExtractForwardedMalformedExplicitEMLDoesNotFallbackToText(t *testing.T) {
	raw := strings.Replace(triggerWithForwardedPart(
		"application/octet-stream",
		`attachment; filename="sent.eml"`,
		"base64",
		"this is not base64 !",
	), "Please receipt this sent mail.", sentMessage("sent-inline@acme.test"), 1)

	if _, err := ExtractForwardedEmail([]byte(raw)); err == nil {
		t.Fatal("malformed explicit .eml attachment should fail closed instead of falling back to text")
	}
}

func triggerWithForwardedPart(contentType, disposition, transferEncoding, payload string) string {
	var b strings.Builder
	b.WriteString(`From: Docketing <docketing@acme.test>
To: receipt@acme.test
Subject: receipt request
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain

Please receipt this sent mail.
--b
Content-Type: ` + contentType + "\n")
	if disposition != "" {
		b.WriteString("Content-Disposition: " + disposition + "\n")
	}
	if transferEncoding != "" {
		b.WriteString("Content-Transfer-Encoding: " + transferEncoding + "\n")
	}
	b.WriteString("\n")
	b.WriteString(payload)
	b.WriteString(`
--b--
`)
	return b.String()
}

func sentMessage(messageID string) string {
	return `From: Attorney <attorney1@acme.test>
To: client@example.test
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <` + messageID + `>

body
`
}
