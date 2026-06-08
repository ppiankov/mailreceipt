package eml

import (
	"strings"
	"testing"
	"time"
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

// WO-25: receipt subjects should be readable even when the original mail uses MIME encoded-words.
func TestParseRFC2047Subjects(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		want    string
	}{
		{name: "plain ascii", subject: "Re: matter", want: "Re: matter"},
		{name: "plain utf8", subject: "Привет", want: "Привет"},
		{name: "utf8 encoded word", subject: "=?UTF-8?B?0J/RgNC40LLQtdGC?=", want: "Привет"},
		{name: "koi8r encoded word", subject: "=?koi8-r?B?8NLJ18XU?=", want: "Привет"},
		{name: "windows1251 encoded word", subject: "=?windows-1251?B?z/Do4uXy?=", want: "Привет"},
		{name: "unknown charset fallback", subject: "=?x-unknown?B?8NLJ18XU?=", want: "=?x-unknown?B?8NLJ18XU?="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `From: Anna <anna@ip.test>
To: jdoe@client.test
Subject: ` + tt.subject + `
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <abc@ip.test>

body here
`
			e, err := Parse(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if e.Subject != tt.want {
				t.Fatalf("subject: want %q, got %q", tt.want, e.Subject)
			}
		})
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
	if e.DateRaw != "Friday, June 5, 2026 3:09 PM" {
		t.Fatalf("date raw: got %q", e.DateRaw)
	}
	want := time.Date(2026, 6, 5, 15, 9, 0, 0, time.UTC)
	if !e.Date.Equal(want) {
		t.Fatalf("lenient sent date: want %s, got %s", want, e.Date)
	}
}

func TestParseLenientMailStyleDate(t *testing.T) {
	raw := `Forwarded message
Date: Fri, 5 Jun 2026 15:09:00 +0000
From: Anna <anna@ip.test>
To: jdoe@client.test
Subject: matter
`
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if e.DateRaw != "Fri, 5 Jun 2026 15:09:00 +0000" {
		t.Fatalf("date raw: got %q", e.DateRaw)
	}
	want := time.Date(2026, 6, 5, 15, 9, 0, 0, time.UTC)
	if !e.Date.Equal(want) {
		t.Fatalf("mail-style lenient date: want %s, got %s", want, e.Date)
	}
}

func TestRecipientsDeduped(t *testing.T) {
	e := Email{To: []string{"A@x.test", "a@x.test"}, Cc: []string{"a@x.test"}}
	if got := e.Recipients(); len(got) != 1 || got[0] != "a@x.test" {
		t.Fatalf("recipients must dedupe case-insensitively, got %v", got)
	}
}
