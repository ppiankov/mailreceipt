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

// WO-37: Outlook forwards can QP-soft-wrap address headers and duplicate each
// recipient as both a mailto URL and a bare addr-spec.
func TestParseOutlookMailtoDoubledSoftWrappedRecipients(t *testing.T) {
	raw := "From: Sender <sender@example.test>\r\n" +
		"To: 'Alpha One' < <mailto:alpha@clientfirm.test> alpha@clientfirm.test>; =\r\n" +
		" 'Beta Two' < <mailto:beta@clientfirm.test> beta@clientfirm.test>\r\n" +
		"Cc: 'Gamma Three' < <mailto:gamma@example.test> gamma@example.test>\r\n" +
		"Subject: Filing\r\n" +
		"Date: Fri, 5 Jun 2026 15:09:00 +0000\r\n" +
		"Message-ID: <outlook-qp@example.test>\r\n" +
		"\r\n" +
		"body\r\n"
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := e.Recipients()
	want := []string{"alpha@clientfirm.test", "beta@clientfirm.test", "gamma@example.test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients: want %v, got %v", want, got)
	}
}

// WO-37: a QP soft break can land mid-address with no following whitespace; the
// repair must rejoin the token instead of dropping the recipient. The
// whitespace-required form missed this and returned zero recipients.
func TestParseMidAddressSoftWrappedRecipient(t *testing.T) {
	raw := "From: Sender <sender@example.test>\r\n" +
		"To: a@client=\r\nfirm.test\r\n" +
		"Subject: Filing\r\n" +
		"Date: Fri, 5 Jun 2026 15:09:00 +0000\r\n" +
		"Message-ID: <mid-softwrap@example.test>\r\n" +
		"\r\n" +
		"body\r\n"
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := e.Recipients()
	want := []string{"a@clientfirm.test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients: want %v, got %v", want, got)
	}
}

// WO-37: Outlook frequently uses semicolons where RFC5322 expects commas.
func TestParseSemicolonSeparatedRecipients(t *testing.T) {
	raw := `From: Sender <sender@example.test>
To: First <first@example.test>; Second <second@example.test>, third@example.test
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <semicolon@example.test>

body
`
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := e.Recipients()
	want := []string{"first@example.test", "second@example.test", "third@example.test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients: want %v, got %v", want, got)
	}
}

// WO-37: valid local-parts may contain =HH bytes; QP repair must not rewrite them.
func TestParsePreservesEqualsHexLocalPart(t *testing.T) {
	raw := `From: Sender <sender@example.test>
To: Case <case=40example@example.test>
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <equals-hex@example.test>

body
`
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := e.Recipients()
	want := []string{"case=40example@example.test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients: want %v, got %v", want, got)
	}
}

// WO-37: fallback must not scan a QP-corrupted copy when raw tokens are valid.
func TestParseFallbackPreservesEqualsHexLocalPart(t *testing.T) {
	tests := []struct {
		name      string
		recipient string
	}{
		{name: "at sign", recipient: "case=40example@example.test"},
		{name: "equals", recipient: "case=3dexample@example.test"},
		{name: "space", recipient: "case=20example@example.test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `From: Sender <sender@example.test>
To: Case <` + tt.recipient + `> trailing text
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <equals-hex-fallback@example.test>

body
`
			e, err := Parse(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			got := e.Recipients()
			want := []string{tt.recipient}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("recipients: want %v, got %v", want, got)
			}
		})
	}
}

// WO-37: valid local-parts may begin with =HH bytes; prefix QP-looking bytes
// must not be treated as encoded delimiters when they are literal addr-spec text.
func TestParseFallbackPreservesEqualsHexPrefixLocalPart(t *testing.T) {
	tests := []struct {
		name      string
		recipient string
	}{
		{name: "equals", recipient: "=3dcase@example.test"},
		{name: "space", recipient: "=20case@example.test"},
		{name: "left angle", recipient: "=3ccase@example.test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `From: Sender <sender@example.test>
To: <` + tt.recipient + `> trailing text
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <equals-hex-prefix@example.test>

body
`
			e, err := Parse(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			got := e.Recipients()
			want := []string{tt.recipient}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("recipients: want %v, got %v", want, got)
			}
		})
	}
}

// WO-37: QP-encoded angle brackets must decode before regex fallback runs.
func TestParseQuotedPrintableAngleRecipient(t *testing.T) {
	raw := `From: Sender <sender@example.test>
To: =3Cjohn@example.test=3E
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <qp-angle@example.test>

body
`
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := e.Recipients()
	want := []string{"john@example.test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients: want %v, got %v", want, got)
	}
}

// WO-37: an encoded angle-delimiter pair nested inside raw angle brackets is
// still a delimiter artifact, not a literal =3C-prefixed local-part.
func TestParseNestedQuotedPrintableAngleRecipient(t *testing.T) {
	raw := `From: Sender <sender@example.test>
To: <=3Cjohn@example.test=3E> trailing text
Subject: Filing
Date: Fri, 5 Jun 2026 15:09:00 +0000
Message-ID: <nested-qp-angle@example.test>

body
`
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := e.Recipients()
	want := []string{"john@example.test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients: want %v, got %v", want, got)
	}
}

// WO-37: lenient pasted blocks need the same folded-header recovery as RFC822.
func TestParseLenientFoldedOutlookRecipients(t *testing.T) {
	raw := `From: Sender <sender@example.test>
Sent: Friday, June 5, 2026 3:09 PM
To: 'Alpha One' < <mailto:alpha@clientfirm.test> alpha@clientfirm.test>; =
  'Beta Two' < <mailto:beta@clientfirm.test> beta@clientfirm.test>
Cc: Gamma <gamma@example.test>; Delta <delta@example.test>
Subject: Filing

body
`
	e, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := e.Recipients()
	want := []string{"alpha@clientfirm.test", "beta@clientfirm.test", "gamma@example.test", "delta@example.test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recipients: want %v, got %v", want, got)
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
