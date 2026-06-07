package eml

import (
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
