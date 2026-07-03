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
	"strings"
	"time"

	"github.com/ppiankov/mailreceipt/internal/eml"
	"github.com/ppiankov/mailreceipt/internal/maillog"
)

// Outcome is the delivery disposition for one recipient.
type Outcome string

const (
	// DeliveredRemote: a remote SMTP/LMTP relay accepted the message (SMTP 2xx) at
	// relay handoff to a remote host[ip]. Transport only — the strongest claim the
	// log supports.
	DeliveredRemote Outcome = "delivered_remote"
	// DeliveredLocal: a local Postfix transport (pipe/local/virtual, or a relay with
	// no remote host[ip] such as relay=mailreceipt) accepted the message. The server
	// handed it to a local mailbox/pipe/service — NOT relayed to a remote mail
	// server, and NOT an SMTP 2xx from a remote MX.
	DeliveredLocal Outcome = "delivered_local"
	// Bounced: hard rejection (5xx) — it will not be delivered.
	Bounced Outcome = "bounced"
	// Deferred: temporary failure; Postfix will retry. Not yet delivered.
	Deferred Outcome = "deferred"
	// NotFound: no matching delivery line in the supplied log. Could mean the
	// message was never sent through this MTA, the log does not cover the period,
	// or matching failed. Explicitly NOT "delivered".
	NotFound Outcome = "not_found"
	// Mixed: recipients resolved to more than one terminal outcome (e.g. some
	// delivered, some not found). The overall verdict is neither — it is the mix.
	// Only ever a whole-email summary, never a per-recipient outcome.
	Mixed Outcome = "mixed"
	// Delivered is a SUMMARY-ONLY value: every recipient was delivered, but across a
	// mix of remote and local handoffs, so neither subtype describes the whole. It
	// is never assigned to a single recipient (each carries delivered_remote or
	// delivered_local). It makes "all delivered" read as success, not as "mixed".
	Delivered Outcome = "delivered"
)

// IsDelivered reports whether an outcome is any delivered-class disposition
// (remote or local). Summary/Counts use this so both count as delivered for the
// overall verdict while the per-recipient distinction is preserved.
func (o Outcome) IsDelivered() bool {
	return o == DeliveredRemote || o == DeliveredLocal
}

// MatchMethod records how the recipient's events were linked, for the receipt's
// provenance.
type MatchMethod string

const (
	MatchMessageID    MatchMethod = "message_id"
	MatchRecipient    MatchMethod = "recipient_window"
	MatchRecipientSet MatchMethod = "recipient_set"
	MatchNone         MatchMethod = "none"
)

// RecipientResult is the cited outcome for one recipient.
type RecipientResult struct {
	Recipient string      `json:"recipient"`
	Outcome   Outcome     `json:"outcome"`
	Match     MatchMethod `json:"match_method"`
	// Relay is the transport that accepted/rejected: a remote host[ip] for remote
	// deliveries, or a local transport name (e.g. mailreceipt, local) for local ones.
	Relay string `json:"relay,omitempty"`
	// Response is the transport's reply text. For a remote delivery this is the
	// remote server's SMTP reply (e.g. "250 2.0.0 OK"); for a local handoff it is the
	// local transport's status text, NOT a remote SMTP reply.
	Response string `json:"response,omitempty"`
	// Time is when the delivery attempt was logged.
	Time time.Time `json:"time,omitempty"`
	// Citation is the verbatim log line this outcome was decided from, or empty
	// for NotFound.
	Citation string `json:"citation,omitempty"`
	// Note carries non-delivery provenance for a NotFound result. WO-33: when the
	// message-id was seen elsewhere in the log (e.g. a scanner/cleanup line) but no
	// delivery event was recorded, this says so — without implying delivery.
	Note string `json:"note,omitempty"`
}

// Result is the full delivery analysis for a dropped email.
type Result struct {
	MessageID  string            `json:"message_id,omitempty"`
	Subject    string            `json:"subject,omitempty"`
	Recipients []RecipientResult `json:"recipients"`
	// Caveat is the standing honesty boundary, included on every result.
	Caveat string `json:"caveat"`
}

// The caveat is attached to every Result so the limit is never implied away. It is
// chosen per result so it never claims a handoff the log did not show. A local-only
// receipt uses localCaveat, which deliberately avoids the literal phrases "remote
// mail server" and "SMTP 2xx" entirely (WO-27): a legal exhibit should not surface
// those words at all when no remote relay was observed. A mixed receipt uses
// mixedCaveat, which scopes each claim to its handoff type.
const remoteCaveat = "A 'delivered' outcome means the remote mail server accepted the message (SMTP 2xx) at relay handoff. It does not prove a person read it, that it passed spam filtering, or that it was not later discarded. This receipt reports transport, not attention."

const localCaveat = "A 'delivered local' outcome means this mail server handed the message to a local transport, mailbox, pipe, or service. No remote relay acceptance was observed. It does not prove onward delivery, human reading, spam placement, or downstream processing. This receipt reports transport, not attention."

const mixedCaveat = "This receipt covers two handoff types. A 'delivered' (remote) outcome means a remote mail server accepted the message at relay handoff; a 'delivered local' outcome means this mail server handed the message to a local transport, with no remote relay acceptance observed. Neither proves a person read it, that it passed spam filtering, or that it was not later discarded. This receipt reports transport, not attention."

// noDeliveryCaveat is used when no recipient was delivered (all not_found, bounced,
// or deferred). WO-32: such a receipt must NOT lead with "a delivered outcome means
// the remote mail server accepted (SMTP 2xx)" — there is no delivery to qualify, so
// it makes no remote/SMTP claim at all.
const noDeliveryCaveat = "This receipt found no delivery record for the message. It reports only what the supplied log shows; absence of a delivery line is not proof the message was or was not delivered elsewhere. This receipt reports transport, not attention."

// caveatFor picks the honest caveat for the set of recipient results. No delivery
// at all => noDeliveryCaveat (no remote/SMTP claim); local-only => localCaveat; any
// mix of remote and local delivery => mixedCaveat (so a local recipient is never
// described as remote acceptance, and vice versa); else the remote caveat.
func caveatFor(recipients []RecipientResult) string {
	anyRemote, anyLocal := false, false
	for _, rr := range recipients {
		switch rr.Outcome {
		case DeliveredRemote:
			anyRemote = true
		case DeliveredLocal:
			anyLocal = true
		}
	}
	switch {
	case !anyRemote && !anyLocal:
		return noDeliveryCaveat
	case anyLocal && !anyRemote:
		return localCaveat
	case anyLocal && anyRemote:
		return mixedCaveat
	default:
		return remoteCaveat
	}
}

// Window is the +/- span around the email's Date used for recipient-fallback
// matching when no Message-ID is available.
const Window = 48 * time.Hour

// Analyze correlates the email against the log and returns a cited result per
// recipient.
func Analyze(e eml.Email, log maillog.Log) Result {
	res := Result{
		MessageID: e.MessageID,
		Subject:   e.Subject,
	}

	recipients := e.Recipients()
	sole := len(recipients) == 1

	// WO-42: when the forwarded copy's Message-ID does not correlate (Outlook
	// strips it, Exchange rewrites it between the Sent copy and the wire), try to
	// pin the message by its full recipient SET before falling to per-recipient
	// windows. If the set uniquely identifies one logged message (one queue-id) in
	// the window, attribute every recipient from that message's events. This is
	// strictly more precise than per-recipient matching and resolves recipients
	// who also received unrelated mail in the window. Only engaged when no
	// recipient correlates by Message-ID, and only on a unique set match.
	if setEvents := setMatchEvents(e, recipients, log); setEvents != nil {
		for _, rcpt := range recipients {
			res.Recipients = append(res.Recipients, resultFromEvents(rcpt, setEvents, MatchRecipientSet, e, log))
		}
	} else {
		for _, rcpt := range recipients {
			res.Recipients = append(res.Recipients, analyzeRecipient(e, rcpt, log, sole))
		}
	}
	// Stable order for deterministic output/tests.
	sort.Slice(res.Recipients, func(i, j int) bool {
		return res.Recipients[i].Recipient < res.Recipients[j].Recipient
	})
	res.Caveat = caveatFor(res.Recipients)
	return res
}

// recipientMatches reports whether a log event's recipient identifies the wanted
// recipient. It matches a full address exactly, and also matches a bare mailbox
// username (as Dovecot logs it, e.g. "clerk") against the local-part of the
// wanted address ("clerk@acme.test"). WO-34: Dovecot LDA often logs the
// bare username, not the full address.
func recipientMatches(evTo, want string) bool {
	evTo = strings.ToLower(strings.TrimSpace(evTo))
	want = strings.ToLower(strings.TrimSpace(want))
	if evTo == "" {
		return false
	}
	if evTo == want {
		return true
	}
	// Bare username (no @) matches the recipient's local-part.
	if !strings.Contains(evTo, "@") {
		if i := strings.Index(want, "@"); i > 0 && evTo == want[:i] {
			return true
		}
	}
	return false
}

// eventMatchesRecipient reports whether a delivery event belongs to the wanted
// recipient, by either the delivered-to address/mailbox (To) or the pre-alias
// original recipient (OrigTo). WO-35: postfix/local logs "to=<alias-target>,
// orig_to=<address>", so an alias delivery (jsmith -> docketing mailbox) correlates
// to the address it was sent to via orig_to — the alias bridge Postfix itself logs.
func eventMatchesRecipient(ev maillog.Event, rcpt string) bool {
	return recipientMatches(ev.To, rcpt) || recipientMatches(ev.OrigTo, rcpt)
}

func analyzeRecipient(e eml.Email, rcpt string, log maillog.Log, sole bool) RecipientResult {
	// 1) Strongest link: Message-ID, then filter to this recipient.
	var events []maillog.Event
	method := MatchNone
	if e.MessageID != "" {
		for _, ev := range log.EventsForMessageID(e.MessageID) {
			// Normal correlation: the event's recipient identifies this recipient.
			// WO-34: a Dovecot save logs the FINAL mailbox name after /etc/aliases
			// remapping, which often differs from the address. The Message-ID is the
			// same across every alias hop, so when this is the message's only
			// recipient, a Message-ID-matched Dovecot save is unambiguously theirs
			// even when the mailbox name does not match the address.
			if eventMatchesRecipient(ev, rcpt) || (sole && ev.Daemon == "dovecot") {
				events = append(events, ev)
			}
		}
		if len(events) > 0 {
			method = MatchMessageID
		}
	}
	// 2) Fallback: recipient within a time window around the send.
	if len(events) == 0 {
		if e.Date.IsZero() {
			// WO-8: no send time means recipient fallback would be unbounded.
			return notFoundResult(rcpt, e.MessageID, log)
		}
		from := e.Date.Add(-Window)
		until := e.Date.Add(Window)
		events = log.EventsForRecipient(rcpt, from, until)
		// WO-41: the window can catch deliveries of DIFFERENT messages to the same
		// recipient. Attribute only when the match is unambiguous — every matched
		// event belongs to a single message (one queue-id). Multiple queue-ids mean
		// we cannot tell which send is the forwarded one, so we refuse rather than
		// guess (mirror, not oracle). Deferred-then-sent attempts of one message
		// share a queue-id and remain a valid single match.
		if len(events) > 0 && !singleQueueID(events) {
			return notFoundResult(rcpt, e.MessageID, log)
		}
		if len(events) > 0 {
			method = MatchRecipient
		}
	}

	if len(events) == 0 {
		return notFoundResult(rcpt, e.MessageID, log)
	}

	// Pick the most decisive, most recent event for this recipient. Precedence:
	// a later attempt supersedes an earlier one (deferred -> later sent), and
	// among same-time events a terminal status wins over deferred.
	chosen := chooseEvent(events)
	return RecipientResult{
		Recipient: rcpt,
		Outcome:   outcomeFor(chosen),
		Match:     method,
		Relay:     chosen.Relay,
		Response:  chosen.Response,
		Time:      chosen.Time,
		Citation:  chosen.RawLine,
	}
}

// setMatchEvents returns the events of the single logged message whose recipient
// set matches the forwarded message's, or nil. WO-42: it engages only when the
// Message-ID does not correlate for ANY recipient (absent, or present but
// matching no log event — Exchange rewrites the id), and a send Date is present
// to bound the window. A nil result means "no unique set match" and the caller
// falls back to per-recipient analysis.
func setMatchEvents(e eml.Email, recipients []string, log maillog.Log) []maillog.Event {
	if e.Date.IsZero() || len(recipients) == 0 {
		return nil
	}
	// If the Message-ID already correlates to any delivery, the exact-id path is
	// authoritative; do not override it with a set guess.
	if e.MessageID != "" && len(log.EventsForMessageID(e.MessageID)) > 0 {
		return nil
	}
	from := e.Date.Add(-Window)
	until := e.Date.Add(Window)
	return log.EventsForRecipientSet(recipients, from, until)
}

// resultFromEvents builds a recipient's result from a pinned set of events (all
// from one message, WO-42). It selects the events addressed to this recipient and
// chooses the decisive one; if none in the pinned set match this recipient, it is
// not found (the set covered the address via a different event form).
func resultFromEvents(rcpt string, events []maillog.Event, method MatchMethod, e eml.Email, log maillog.Log) RecipientResult {
	var mine []maillog.Event
	for _, ev := range events {
		if eventMatchesRecipient(ev, rcpt) {
			mine = append(mine, ev)
		}
	}
	if len(mine) == 0 {
		return notFoundResult(rcpt, e.MessageID, log)
	}
	chosen := chooseEvent(mine)
	return RecipientResult{
		Recipient: rcpt,
		Outcome:   outcomeFor(chosen),
		Match:     method,
		Relay:     chosen.Relay,
		Response:  chosen.Response,
		Time:      chosen.Time,
		Citation:  chosen.RawLine,
	}
}

// notFoundResult builds a NotFound for a recipient. WO-33: if the message-id was
// seen anywhere in the log (e.g. a scanner/cleanup line) but no delivery event was
// recorded, it adds a note saying so — without implying delivery.
func notFoundResult(rcpt, messageID string, log maillog.Log) RecipientResult {
	rr := RecipientResult{Recipient: rcpt, Outcome: NotFound, Match: MatchNone}
	if messageID != "" && log.SawMessageID(messageID) {
		rr.Note = "message seen in the log, but no delivery event was recorded for this recipient"
	}
	return rr
}

// singleQueueID reports whether every event belongs to the same Postfix queue-id,
// i.e. one message. WO-41: the recipient+date-window fallback may catch deliveries
// of different messages to the same recipient; a single queue-id means the window
// matched exactly one message (deferred-then-sent attempts of that message share
// the id), so attribution is unambiguous. An empty queue-id (e.g. a Dovecot local
// save) is treated as its own group, so a mix with empties is not unique.
func singleQueueID(events []maillog.Event) bool {
	if len(events) == 0 {
		return false
	}
	first := events[0].QueueID
	for _, ev := range events[1:] {
		if ev.QueueID != first {
			return false
		}
	}
	return true
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

func outcomeFor(e maillog.Event) Outcome {
	switch e.Status {
	case maillog.StatusSent:
		// status=sent through a remote network agent (smtp/lmtp) to a remote
		// host[ip] is a remote-MX handoff. A local agent (pipe/local/virtual), or a
		// relay with no remote host[ip] (e.g. relay=mailreceipt, relay=local), is a
		// local handoff — a real delivery, but NOT to a remote mail server.
		if isRemoteHandoff(e.Daemon, e.Relay) {
			return DeliveredRemote
		}
		return DeliveredLocal
	case maillog.StatusBounced:
		return Bounced
	case maillog.StatusDeferred:
		return Deferred
	default:
		return NotFound
	}
}

// isRemoteHandoff reports whether a status=sent event was relayed to a remote
// mail server: a network delivery agent (smtp/lmtp) and a relay naming a remote
// host with an [ip] literal. Postfix writes the remote relay as host[ip]:port; a
// local transport writes a bare name (pipe, local, virtual) or relay=local with
// no bracketed address.
func isRemoteHandoff(daemon, relay string) bool {
	switch daemon {
	case "smtp", "lmtp":
	default:
		return false
	}
	// A remote relay carries a bracketed IP literal, e.g. mx.example[1.2.3.4]:25.
	// relay=local / relay=mailreceipt / relay=none have no [ip] and are not remote.
	if !strings.Contains(relay, "[") {
		return false
	}
	r := strings.ToLower(strings.TrimSpace(relay))
	if r == "" || strings.HasPrefix(r, "local") || strings.HasPrefix(r, "none") {
		return false
	}
	return true
}

// Summary reduces the per-recipient results to a single headline outcome for the
// whole email. A hard bounce always surfaces; else a pending deferral; else, when
// every recipient shares one outcome, that outcome; else "mixed". The headline is
// a faithful compression of the rows — never the best case, never the worst, the
// actual case. Per-recipient detail and counts (Counts) carry the specifics.
func (r Result) Summary() Outcome {
	c := r.Counts()
	switch {
	case c[Bounced] > 0:
		// A hard bounce must always surface as the headline.
		return Bounced
	case c[Deferred] > 0 && c[Bounced] == 0:
		return Deferred
	case len(c) == 0:
		return NotFound
	}
	// All-delivered (only delivered-class outcomes present) reads as delivered, even
	// if the recipients split remote/local — that is success, not a partial mix.
	delivered := c[DeliveredRemote] + c[DeliveredLocal]
	if delivered > 0 && delivered == r.recipientCount() {
		switch {
		case c[DeliveredLocal] == 0:
			return DeliveredRemote
		case c[DeliveredRemote] == 0:
			return DeliveredLocal
		default:
			return Delivered // all delivered, mixed transports
		}
	}
	if len(c) == 1 {
		// Exactly one distinct outcome across all recipients (e.g. all not_found).
		for o := range c {
			return o
		}
	}
	// More than one distinct outcome including a non-delivered one (e.g. delivered +
	// not_found): the verdict is the mix, not the best or worst single row.
	return Mixed
}

func (r Result) recipientCount() int { return len(r.Recipients) }

// Counts tallies per-recipient outcomes. The headline and JSON summary_counts are
// both derived from this, so the summary can never contradict the rows.
func (r Result) Counts() map[Outcome]int {
	c := map[Outcome]int{}
	for _, rr := range r.Recipients {
		c[rr.Outcome]++
	}
	return c
}
