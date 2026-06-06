package eml

import (
	"strings"
	"testing"
)

func TestParseRFC822(t *testing.T) {
	raw := `From: Anna <anna@ip.test>
To: jdoe@client.test
Cc: team@client.test
Subject: Re: matter
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <abc@ip.test>

body here
`
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if e.MessageID != "abc@ip.test" {
		t.Fatalf("message-id: got %q", e.MessageID)
	}
	got := e.Recipients()
	if len(got) != 2 || got[0] != "jdoe@client.test" || got[1] != "team@client.test" {
		t.Fatalf("recipients: got %v", got)
	}
}

func TestParseLenientPastedBlock(t *testing.T) {
	// No real Message-ID; pasted forwarded headers.
	raw := `From: Anna Petrova <anna@ip.test>
Sent: Friday, June 5, 2026 3:09 PM
To: 'jdoe@client.test' <jdoe@client.test>
Cc: 'team@client.test' <team@client.test>
Subject: URGENT: matter

Dear Colleagues,
`
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if e.MessageID != "" {
		t.Fatalf("pasted block has no message-id, got %q", e.MessageID)
	}
	got := e.Recipients()
	if len(got) != 2 {
		t.Fatalf("lenient recipients: got %v", got)
	}
	if got[0] != "jdoe@client.test" {
		t.Fatalf("lenient recipient extraction: got %v", got)
	}
}

func TestRecipientsDeduped(t *testing.T) {
	e := Email{To: []string{"A@x.test", "a@x.test"}, Cc: []string{"a@x.test"}}
	if got := e.Recipients(); len(got) != 1 || got[0] != "a@x.test" {
		t.Fatalf("recipients must dedupe case-insensitively, got %v", got)
	}
}
