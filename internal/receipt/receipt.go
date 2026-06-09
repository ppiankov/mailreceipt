// Package receipt renders a delivery analysis into the two artifact forms a
// business attaches to a case: a human-readable Markdown receipt and a
// machine-readable JSON receipt. Both carry every citation; neither adds a fact
// the deliver engine did not produce.
package receipt

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ppiankov/mailreceipt/internal/deliver"
)

// Receipt is the attachable artifact. It is a thin envelope over the delivery
// Result plus generation metadata, so the JSON form is self-describing.
type Receipt struct {
	ArtifactType string          `json:"artifact_type"`
	Tool         string          `json:"tool"`
	GeneratedAt  string          `json:"generated_at,omitempty"`
	Case         string          `json:"case,omitempty"`
	Summary      deliver.Outcome `json:"summary"`
	// SummaryCounts is the per-outcome tally the summary is derived from, so a
	// machine consumer can see the mix behind a "mixed" summary without reparsing
	// the rows. Keyed by outcome string (e.g. "delivered", "not_found").
	SummaryCounts map[string]int `json:"summary_counts"`
	Result        deliver.Result `json:"result"`
}

// New builds a Receipt from a delivery Result. generatedAt is passed in (not read
// from the clock) so callers control determinism; pass the zero time to omit it.
func New(res deliver.Result, caseRef string, generatedAt time.Time) Receipt {
	r := Receipt{
		ArtifactType:  "mail_delivery_receipt",
		Tool:          "mailreceipt",
		Case:          caseRef,
		Summary:       res.Summary(),
		SummaryCounts: countsJSON(res.Counts()),
		Result:        res,
	}
	if !generatedAt.IsZero() {
		r.GeneratedAt = generatedAt.UTC().Format(time.RFC3339)
	}
	return r
}

// JSON renders the receipt as indented JSON.
func (r Receipt) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Markdown renders the human-readable receipt with one cited line per recipient.
func (r Receipt) Markdown() string {
	var b strings.Builder
	b.WriteString("# Mail Delivery Receipt\n\n")
	if r.Case != "" {
		fmt.Fprintf(&b, "**Case:** %s  \n", r.Case)
	}
	if r.Result.Subject != "" {
		fmt.Fprintf(&b, "**Subject:** %s  \n", r.Result.Subject)
	}
	if r.Result.MessageID != "" {
		fmt.Fprintf(&b, "**Message-ID:** %s  \n", r.Result.MessageID)
	}
	fmt.Fprintf(&b, "**Overall:** %s\n\n", headline(r.Summary, r.Result.Counts()))

	b.WriteString("| Recipient | Outcome | When | Evidence |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, rr := range r.Result.Recipients {
		when := "—"
		if !rr.Time.IsZero() {
			when = rr.Time.Format("2006-01-02 15:04")
		}
		ev := rr.Response
		if ev == "" {
			if rr.Outcome == deliver.NotFound {
				ev = "no matching line in log"
			} else {
				ev = "(see citation)"
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
			rr.Recipient, badge(rr.Outcome), when, mdEscape(ev))
	}

	b.WriteString("\n## Evidence (verbatim log lines)\n\n")
	any := false
	for _, rr := range r.Result.Recipients {
		if rr.Citation == "" {
			continue
		}
		any = true
		fmt.Fprintf(&b, "- **%s** (matched by %s):\n\n  ```\n  %s\n  ```\n",
			rr.Recipient, rr.Match, rr.Citation)
	}
	if !any {
		b.WriteString("_No matching delivery lines were found in the supplied log._\n")
	}

	fmt.Fprintf(&b, "\n> %s\n", r.Result.Caveat)
	return b.String()
}

// PlainText renders the receipt for conservative mail clients. WO-26: filter
// replies must remain readable when the client shows raw text/plain without
// Markdown rendering.
func (r Receipt) PlainText() string {
	var b strings.Builder
	b.WriteString("MAIL DELIVERY RECEIPT\n\n")
	b.WriteString("MESSAGE\n")
	if r.Case != "" {
		fmt.Fprintf(&b, "  Case: %s\n", r.Case)
	}
	if r.Result.Subject != "" {
		fmt.Fprintf(&b, "  Subject: %s\n", r.Result.Subject)
	}
	if r.Result.MessageID != "" {
		fmt.Fprintf(&b, "  Message-ID: %s\n", r.Result.MessageID)
	}
	fmt.Fprintf(&b, "  Overall: %s\n\n", plainHeadline(r.Summary, r.Result.Counts()))

	b.WriteString("RECIPIENTS\n")
	for i, rr := range r.Result.Recipients {
		if i > 0 {
			b.WriteString("\n")
		}
		when := "not recorded"
		if !rr.Time.IsZero() {
			when = rr.Time.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "  Recipient: %s\n", rr.Recipient)
		fmt.Fprintf(&b, "  Outcome: %s\n", plainOutcome(rr.Outcome))
		fmt.Fprintf(&b, "  When: %s\n", when)
		fmt.Fprintf(&b, "  Evidence: %s\n", plainEvidence(rr))
		if rr.Match != deliver.MatchNone {
			fmt.Fprintf(&b, "  Match: %s\n", rr.Match)
		}
	}

	b.WriteString("\nEVIDENCE\n")
	any := false
	for _, rr := range r.Result.Recipients {
		if rr.Citation == "" {
			continue
		}
		any = true
		fmt.Fprintf(&b, "  %s (matched by %s):\n", rr.Recipient, rr.Match)
		fmt.Fprintf(&b, "    %s\n", rr.Citation)
	}
	if !any {
		b.WriteString("  No matching delivery lines were found in the supplied log.\n")
	}

	b.WriteString("\nLIMITATION\n")
	fmt.Fprintf(&b, "  %s\n", r.Result.Caveat)
	return b.String()
}

// headline gives a short status sentence for the overall summary. For a mixed
// verdict it states the per-outcome counts so the headline faithfully compresses
// the rows rather than collapsing to one of them.
func headline(o deliver.Outcome, counts map[deliver.Outcome]int) string {
	switch o {
	case deliver.Delivered:
		return "Delivered — accepted at relay handoff (remote and local recipients)"
	case deliver.DeliveredRemote:
		return "Delivered — accepted by the remote mail server"
	case deliver.DeliveredLocal:
		return "Delivered locally — accepted by a local mail transport"
	case deliver.Bounced:
		return "Bounced — hard-rejected, not delivered"
	case deliver.Deferred:
		return "Deferred — temporary failure, retrying (not yet delivered)"
	case deliver.NotFound:
		return "Not found — no matching delivery record in the log"
	case deliver.Mixed:
		return "Mixed — " + countsPhrase(counts)
	default:
		return string(o)
	}
}

// outcomeOrder fixes the display order so mixed headlines and JSON are
// deterministic regardless of recipient order.
var outcomeOrder = []deliver.Outcome{
	deliver.DeliveredRemote, deliver.DeliveredLocal, deliver.Bounced, deliver.Deferred, deliver.NotFound,
}

var outcomeWords = map[deliver.Outcome]string{
	deliver.DeliveredRemote: "delivered (remote)",
	deliver.DeliveredLocal:  "delivered (local)",
	deliver.Bounced:         "bounced",
	deliver.Deferred:        "deferred",
	deliver.NotFound:        "not found",
}

// countsPhrase renders "4 delivered, 1 not found" in a stable outcome order.
func countsPhrase(counts map[deliver.Outcome]int) string {
	var parts []string
	for _, o := range outcomeOrder {
		if n := counts[o]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, outcomeWords[o]))
		}
	}
	return strings.Join(parts, ", ")
}

func plainHeadline(o deliver.Outcome, counts map[deliver.Outcome]int) string {
	switch o {
	case deliver.Delivered:
		return "DELIVERED - accepted at relay handoff (remote and local recipients)"
	case deliver.DeliveredRemote:
		return "DELIVERED - accepted by the remote mail server"
	case deliver.DeliveredLocal:
		return "DELIVERED LOCAL - accepted by a local mail transport"
	case deliver.Bounced:
		return "BOUNCED - hard-rejected, not delivered"
	case deliver.Deferred:
		return "DEFERRED - temporary failure, retrying (not yet delivered)"
	case deliver.NotFound:
		return "NOT FOUND - no matching delivery record in the log"
	case deliver.Mixed:
		return "MIXED - " + countsPhrase(counts)
	default:
		return strings.ToUpper(string(o))
	}
}

func plainOutcome(o deliver.Outcome) string {
	switch o {
	case deliver.DeliveredRemote:
		return "DELIVERED"
	case deliver.DeliveredLocal:
		return "DELIVERED_LOCAL"
	case deliver.Bounced:
		return "BOUNCED"
	case deliver.Deferred:
		return "DEFERRED"
	case deliver.NotFound:
		return "NOT FOUND"
	default:
		return strings.ToUpper(string(o))
	}
}

func plainEvidence(rr deliver.RecipientResult) string {
	// A local handoff has no remote SMTP reply to quote; name the local transport
	// instead so the receipt never implies a remote-server response it did not see.
	if rr.Outcome == deliver.DeliveredLocal {
		if rr.Relay != "" {
			return "accepted by local mail transport " + rr.Relay
		}
		return "accepted by a local mail transport"
	}
	if rr.Response != "" {
		return rr.Response
	}
	if rr.Outcome == deliver.NotFound {
		return "no matching line in log"
	}
	return "see evidence section"
}

// countsJSON converts the outcome tally to string-keyed counts for the JSON
// artifact, keyed by the per-recipient outcome values
// (e.g. {"delivered_remote": 4, "not_found": 1}).
func countsJSON(counts map[deliver.Outcome]int) map[string]int {
	out := make(map[string]int, len(counts))
	for o, n := range counts {
		out[string(o)] = n
	}
	return out
}

func badge(o deliver.Outcome) string {
	switch o {
	case deliver.DeliveredRemote:
		return "✅ delivered"
	case deliver.DeliveredLocal:
		return "📥 delivered (local)"
	case deliver.Bounced:
		return "⛔ bounced"
	case deliver.Deferred:
		return "⏳ deferred"
	case deliver.NotFound:
		return "❓ not found"
	default:
		return string(o)
	}
}

func mdEscape(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
