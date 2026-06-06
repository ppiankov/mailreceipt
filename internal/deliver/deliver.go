// Package deliver correlates a dropped email with parsed mail-log events and
// computes a per-recipient delivery outcome.
//
// The correlation is deterministic and evidence-first:
//   - prefer an exact Message-ID match (the strongest link Postfix gives us);
//   - else fall back to recipient address within a time window around the send.
//
// Every outcome carries the verbatim log line it was decided from (or records
// that NO line was found). The engine never infers an outcome it cannot cite:
// "not found in the log" is a first-class result, not an error and not a guess.
//
// Honesty boundary (kept on every receipt): a "delivered" outcome means the
// remote mail server ACCEPTED the message (an SMTP 2xx at relay handoff). It does
// NOT prove a human read it, that it cleared the recipient's spam filter, or that
// it was not later discarded. mailreceipt reports transport, not attention.
package deliver

import (
	"sort"
	"time"

	"github.com/ppiankov/mailreceipt/internal/eml"
	"github.com/ppiankov/mailreceipt/internal/maillog"
)

// Outcome is the delivery disposition for one recipient.
type Outcome string

const (
	// Delivered: the remote MX accepted the message (SMTP 2xx). Transport only.
	Delivered Outcome = "delivered"
	// Bounced: hard rejection (5xx) — it will not be delivered.
	Bounced Outcome = "bounced"
	// Deferred: temporary failure; Postfix will retry. Not yet delivered.
	Deferred Outcome = "deferred"
	// NotFound: no matching delivery line in the supplied log. Could mean the
	// message was never sent through this MTA, the log does not cover the period,
	// or matching failed. Explicitly NOT "delivered".
	NotFound Outcome = "not_found"
)

// MatchMethod records how the recipient's events were linked, for the receipt's
// provenance.
type MatchMethod string

const (
	MatchMessageID MatchMethod = "message_id"
	MatchRecipient MatchMethod = "recipient_window"
	MatchNone      MatchMethod = "none"
)

// RecipientResult is the cited outcome for one recipient.
type RecipientResult struct {
	Recipient string      `json:"recipient"`
	Outcome   Outcome     `json:"outcome"`
	Match     MatchMethod `json:"match_method"`
	// Relay is the remote server that accepted/rejected, when known.
	Relay string `json:"relay,omitempty"`
	// Response is the remote server's SMTP reply text (the literal proof).
	Response string `json:"response,omitempty"`
	// Time is when the delivery attempt was logged.
	Time time.Time `json:"time,omitempty"`
	// Citation is the verbatim log line this outcome was decided from, or empty
	// for NotFound.
	Citation string `json:"citation,omitempty"`
}

// Result is the full delivery analysis for a dropped email.
type Result struct {
	MessageID  string            `json:"message_id,omitempty"`
	Subject    string            `json:"subject,omitempty"`
	Recipients []RecipientResult `json:"recipients"`
	// Caveat is the standing honesty boundary, included on every result.
	Caveat string `json:"caveat"`
}

// transportCaveat is attached to every Result so the limit is never implied
// away: the log proves handoff to the remote MX, not human attention.
const transportCaveat = "A 'delivered' outcome means the remote mail server accepted the message (SMTP 2xx) at relay handoff. It does not prove a person read it, that it passed spam filtering, or that it was not later discarded. This receipt reports transport, not attention."

// Window is the +/- span around the email's Date used for recipient-fallback
// matching when no Message-ID is available.
const Window = 48 * time.Hour

// Analyze correlates the email against the log and returns a cited result per
// recipient.
func Analyze(e eml.Email, log maillog.Log) Result {
	res := Result{
		MessageID: e.MessageID,
		Subject:   e.Subject,
		Caveat:    transportCaveat,
	}

	for _, rcpt := range e.Recipients() {
		res.Recipients = append(res.Recipients, analyzeRecipient(e, rcpt, log))
	}
	// Stable order for deterministic output/tests.
	sort.Slice(res.Recipients, func(i, j int) bool {
		return res.Recipients[i].Recipient < res.Recipients[j].Recipient
	})
	return res
}

func analyzeRecipient(e eml.Email, rcpt string, log maillog.Log) RecipientResult {
	// 1) Strongest link: Message-ID, then filter to this recipient.
	var events []maillog.Event
	method := MatchNone
	if e.MessageID != "" {
		for _, ev := range log.EventsForMessageID(e.MessageID) {
			if ev.To == rcpt {
				events = append(events, ev)
			}
		}
		if len(events) > 0 {
			method = MatchMessageID
		}
	}
	// 2) Fallback: recipient within a time window around the send.
	if len(events) == 0 {
		var from, until time.Time
		if !e.Date.IsZero() {
			from = e.Date.Add(-Window)
			until = e.Date.Add(Window)
		}
		events = log.EventsForRecipient(rcpt, from, until)
		if len(events) > 0 {
			method = MatchRecipient
		}
	}

	if len(events) == 0 {
		return RecipientResult{
			Recipient: rcpt,
			Outcome:   NotFound,
			Match:     MatchNone,
		}
	}

	// Pick the most decisive, most recent event for this recipient. Precedence:
	// a later attempt supersedes an earlier one (deferred -> later sent), and
	// among same-time events a terminal status wins over deferred.
	chosen := chooseEvent(events)
	return RecipientResult{
		Recipient: rcpt,
		Outcome:   outcomeFor(chosen.Status),
		Match:     method,
		Relay:     chosen.Relay,
		Response:  chosen.Response,
		Time:      chosen.Time,
		Citation:  chosen.RawLine,
	}
}

// chooseEvent selects the authoritative event: latest by time; if times tie or
// are absent, a terminal status (sent/bounced) outranks deferred.
func chooseEvent(events []maillog.Event) maillog.Event {
	best := events[0]
	for _, e := range events[1:] {
		if isMoreAuthoritative(e, best) {
			best = e
		}
	}
	return best
}

func isMoreAuthoritative(candidate, current maillog.Event) bool {
	// Later attempt wins.
	if !candidate.Time.IsZero() && !current.Time.IsZero() {
		if candidate.Time.After(current.Time) {
			return true
		}
		if candidate.Time.Before(current.Time) {
			return false
		}
	}
	// Same/!unknown time: terminal beats deferred.
	return terminalRank(candidate.Status) > terminalRank(current.Status)
}

func terminalRank(s maillog.Status) int {
	switch s {
	case maillog.StatusSent, maillog.StatusBounced:
		return 2
	case maillog.StatusDeferred:
		return 1
	default:
		return 0
	}
}

func outcomeFor(s maillog.Status) Outcome {
	switch s {
	case maillog.StatusSent:
		return Delivered
	case maillog.StatusBounced:
		return Bounced
	case maillog.StatusDeferred:
		return Deferred
	default:
		return NotFound
	}
}

// Summary reduces the per-recipient results to a single headline outcome for the
// whole email: bounced if any hard-failed, else deferred if any pending, else
// not_found if any recipient is missing, else delivered.
func (r Result) Summary() Outcome {
	var anyDeferred, anyNotFound, anyDelivered bool
	for _, rr := range r.Recipients {
		switch rr.Outcome {
		case Bounced:
			return Bounced
		case Deferred:
			anyDeferred = true
		case NotFound:
			anyNotFound = true
		case Delivered:
			anyDelivered = true
		}
	}
	switch {
	case anyDeferred:
		return Deferred
	case anyNotFound:
		return NotFound
	case anyDelivered:
		return Delivered
	default:
		return NotFound
	}
}
