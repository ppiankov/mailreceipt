package receipt

import (
	"strings"
	"testing"
	"time"

	"github.com/ppiankov/mailreceipt/internal/deliver"
)

func sampleReceipt() Receipt {
	res := deliver.Result{
		MessageID: "m1@ex.test",
		Subject:   "Test",
		Caveat:    "transport, not attention",
		Recipients: []deliver.RecipientResult{
			{
				Recipient: "a@client.test",
				Outcome:   deliver.Delivered,
				Match:     deliver.MatchMessageID,
				Response:  "250 2.0.0 OK",
				Citation:  "Jun  5 02:37:14 mail01 postfix/smtp[20120]: 4F1A2B3C01: to=<a@client.test>, status=sent (250 2.0.0 OK)",
			},
		},
	}
	return New(res, "CASE-1", time.Time{})
}

func TestMarkdownCitesEvidence(t *testing.T) {
	md := sampleReceipt().Markdown()
	if !strings.Contains(md, "status=sent (250 2.0.0 OK)") {
		t.Fatal("markdown must include the verbatim evidence line")
	}
	if !strings.Contains(md, "transport, not attention") {
		t.Fatal("markdown must carry the caveat")
	}
}

func TestVerifyCitationsPassesWhenPresent(t *testing.T) {
	r := sampleReceipt()
	log := "noise\n" + r.Result.Recipients[0].Citation + "\nmore noise\n"
	if missing := r.VerifyCitations(log); len(missing) != 0 {
		t.Fatalf("citations present in log should verify, missing=%v", missing)
	}
}

func TestVerifyCitationsFailsWhenAbsent(t *testing.T) {
	r := sampleReceipt()
	if missing := r.VerifyCitations("a completely different log\n"); len(missing) != 1 {
		t.Fatalf("a fabricated/edited citation must fail verification, got missing=%v", missing)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	r := sampleReceipt()
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"artifact_type": "mail_delivery_receipt"`) {
		t.Fatal("json must self-describe its artifact type")
	}
}
