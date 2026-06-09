package receipt

import (
	"strings"
	"testing"
	"time"

	"github.com/ppiankov/mailreceipt/internal/deliver"
	"github.com/ppiankov/mailreceipt/internal/eml"
	"github.com/ppiankov/mailreceipt/internal/maillog"
)

func sampleReceipt() Receipt {
	res := deliver.Result{
		MessageID: "m1@ex.test",
		Subject:   "Test",
		Caveat:    "transport, not attention",
		Recipients: []deliver.RecipientResult{
			{
				Recipient: "a@client.test",
				Outcome:   deliver.DeliveredRemote,
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

func TestPlainTextIsFormalMailClientReceipt(t *testing.T) {
	txt := sampleReceipt().PlainText()
	for _, want := range []string{
		"MAIL DELIVERY RECEIPT",
		"MESSAGE",
		"  Case: CASE-1",
		"  Subject: Test",
		"  Message-ID: m1@ex.test",
		"  Overall: DELIVERED - accepted by the remote mail server",
		"RECIPIENTS",
		"  Recipient: a@client.test",
		"  Outcome: DELIVERED",
		"  Evidence: 250 2.0.0 OK",
		"EVIDENCE",
		"    Jun  5 02:37:14 mail01 postfix/smtp",
		"LIMITATION",
		"  transport, not attention",
	} {
		if !strings.Contains(txt, want) {
			t.Fatalf("plain text receipt missing %q:\n%s", want, txt)
		}
	}
	for _, forbidden := range []string{"# ", "**", "| Recipient |", "```", "✅", "⛔", "⏳", "❓"} {
		if strings.Contains(txt, forbidden) {
			t.Fatalf("plain text receipt must not require Markdown/glyph rendering; found %q:\n%s", forbidden, txt)
		}
	}
}

func TestPlainTextNotFoundUsesWordsAndNoTable(t *testing.T) {
	res := deliver.Result{
		MessageID: "missing@ex.test",
		Subject:   "Missing",
		Caveat:    "transport, not attention",
		Recipients: []deliver.RecipientResult{{
			Recipient: "ghost@client.test",
			Outcome:   deliver.NotFound,
			Match:     deliver.MatchNone,
		}},
	}
	txt := New(res, "", time.Time{}).PlainText()
	for _, want := range []string{
		"  Overall: NOT FOUND - no matching delivery record in the log",
		"  Outcome: NOT FOUND",
		"  Evidence: no matching line in log",
		"  No matching delivery lines were found in the supplied log.",
	} {
		if !strings.Contains(txt, want) {
			t.Fatalf("plain text not-found receipt missing %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "|") || strings.Contains(txt, "❓") {
		t.Fatalf("plain text not-found receipt should not look like Markdown table output:\n%s", txt)
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
			Outcome:   deliver.DeliveredRemote,
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
	if !strings.Contains(md, "**Overall:** Mixed — 4 delivered (remote), 1 not found") {
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
		!strings.Contains(s, `"delivered_remote": 4`) ||
		!strings.Contains(s, `"not_found": 1`) {
		t.Fatalf("json must carry summary_counts with per-outcome tallies:\n%s", s)
	}
}

// WO-27: a local-only delivery receipt must render the local wording and must NEVER
// say "remote mail server" or "SMTP 2xx" in any part (headline, outcome, evidence,
// or caveat).
func localReceipt() Receipt {
	// Exercise the real path: a local pipe handoff (relay=mailreceipt, postfix/pipe)
	// analyzed end-to-end, so the caveat is the genuine local caveat.
	log := maillog.Parse(strings.NewReader(
		"2026-06-08T19:08:55+02:00 mail postfix/cleanup[1]: 90D9F160065F: message-id=<local-1@acme.test>\n"+
			"2026-06-08T19:08:55+02:00 mail postfix/pipe[2]: 90D9F160065F: to=<receipt@acme.test>, relay=mailreceipt, status=sent (delivered via mailreceipt service)\n",
	), 2026)
	res := deliver.Analyze(eml.Email{MessageID: "local-1@acme.test", To: []string{"receipt@acme.test"}}, log)
	return New(res, "CASE-LOCAL", time.Time{})
}

func TestLocalReceiptNeverClaimsRemote(t *testing.T) {
	// WO-27 rev4: a local-only receipt must not render the literal phrases AT ALL —
	// not even in a negating clause — in any part (headline, outcome, evidence,
	// caveat/limitation) of either rendering.
	for _, body := range []string{localReceipt().Markdown(), localReceipt().PlainText()} {
		for _, bad := range []string{"remote mail server", "SMTP 2xx"} {
			if strings.Contains(body, bad) {
				t.Fatalf("local receipt must not contain %q:\n%s", bad, body)
			}
		}
	}
	pt := localReceipt().PlainText()
	if !strings.Contains(pt, "DELIVERED LOCAL") && !strings.Contains(pt, "DELIVERED_LOCAL") {
		t.Fatalf("local plain-text receipt should name the local delivery, got:\n%s", pt)
	}
}

// WO-32: an all-not_found receipt must not lead its limitation with a remote
// delivery claim; there is no delivery to qualify.
func TestNotFoundReceiptCaveatMakesNoRemoteClaim(t *testing.T) {
	res := deliver.Analyze(
		eml.Email{MessageID: "nf@acme.test", To: []string{"ghost@acme.test"}},
		maillog.Parse(strings.NewReader(
			"Jun  5 10:00:00 mail postfix/smtp[1]: AAAAAA: to=<other@x.test>, relay=mx[1.2.3.4]:25, status=sent (250 OK)\n",
		), 2026),
	)
	r := New(res, "", time.Time{})
	for _, body := range []string{r.Markdown(), r.PlainText()} {
		for _, bad := range []string{"remote mail server", "SMTP 2xx", "a 'delivered' outcome means"} {
			if strings.Contains(strings.ToLower(body), strings.ToLower(bad)) {
				t.Fatalf("not_found receipt must not claim %q:\n%s", bad, body)
			}
		}
	}
}
