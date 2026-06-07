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

func TestVerifyCitationsPassesWithLineEndingAndOuterWhitespaceNormalization(t *testing.T) {
	r := sampleReceipt()
	log := "noise\r\n  " + r.Result.Recipients[0].Citation + "  \r\nmore noise\r\n"
	if missing := r.VerifyCitations(log); len(missing) != 0 {
		t.Fatalf("citation with equivalent outer whitespace should verify, missing=%v", missing)
	}
}

func TestVerifyCitationsFailsWhenAbsent(t *testing.T) {
	r := sampleReceipt()
	if missing := r.VerifyCitations("a completely different log\n"); len(missing) != 1 {
		t.Fatalf("a fabricated/edited citation must fail verification, got missing=%v", missing)
	}
}

func TestVerifyCitationsFailsForTruncatedCitation(t *testing.T) {
	r := sampleReceipt()
	fullCitation := r.Result.Recipients[0].Citation
	r.Result.Recipients[0].Citation = "postfix/smtp[20120]: 4F1A2B3C01: to=<a@client.test>, status=sent (250 2.0.0 OK)"
	log := "noise\n" + fullCitation + "\nmore noise\n"
	if missing := r.VerifyCitations(log); len(missing) != 1 {
		t.Fatalf("truncated citation substring must fail verification, got missing=%v", missing)
	}
}

func TestVerifyCitationsFailsForEditedCitation(t *testing.T) {
	r := sampleReceipt()
	fullCitation := r.Result.Recipients[0].Citation
	r.Result.Recipients[0].Citation = strings.ReplaceAll(fullCitation, "250 2.0.0 OK", "250 2.0.0 ACCEPTED")
	log := "noise\n" + fullCitation + "\nmore noise\n"
	if missing := r.VerifyCitations(log); len(missing) != 1 {
		t.Fatalf("edited citation must fail verification, got missing=%v", missing)
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

// mixedReceipt mirrors the real incident: 4 delivered + 1 not_found.
func mixedReceipt() Receipt {
	var rr []deliver.RecipientResult
	for i := 0; i < 4; i++ {
		rr = append(rr, deliver.RecipientResult{
			Recipient: string(rune('a'+i)) + "@client.test",
			Outcome:   deliver.Delivered,
			Response:  "250 2.6.0 Queued mail for delivery",
		})
	}
	rr = append(rr, deliver.RecipientResult{
		Recipient: "internal@acme.test",
		Outcome:   deliver.NotFound,
	})
	return New(deliver.Result{Caveat: "transport, not attention", Recipients: rr}, "CASE-001", time.Time{})
}

func TestMixedHeadlineStatesCountsAndDoesNotContradictTable(t *testing.T) {
	md := mixedReceipt().Markdown()
	if !strings.Contains(md, "**Overall:** Mixed — 4 delivered, 1 not found") {
		t.Fatalf("mixed headline must state the counts; got markdown:\n%s", md)
	}
	// The regression we are guarding: the headline must NOT say "Not found"
	// while four rows are delivered.
	if strings.Contains(md, "**Overall:** Not found") {
		t.Fatal("headline must not contradict delivered rows")
	}
}

func TestMixedSummaryIsMixed(t *testing.T) {
	if got := mixedReceipt().Summary; got != deliver.Mixed {
		t.Fatalf("summary should be mixed, got %s", got)
	}
}

func TestSummaryCountsInJSON(t *testing.T) {
	b, err := mixedReceipt().JSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"summary": "mixed"`) {
		t.Fatalf("json summary should be mixed:\n%s", s)
	}
	if !strings.Contains(s, `"summary_counts"`) ||
		!strings.Contains(s, `"delivered": 4`) ||
		!strings.Contains(s, `"not_found": 1`) {
		t.Fatalf("json must carry summary_counts with per-outcome tallies:\n%s", s)
	}
}
