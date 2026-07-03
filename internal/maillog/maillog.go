// Package maillog parses Postfix syslog output into delivery events.
//
// Postfix writes one message's lifecycle across several lines tied together by a
// queue id (e.g. "4F1A2B3C"). The lines we care about:
//
//	cleanup: ... <queue-id>: message-id=<...>
//	smtp/lmtp/local: ... <queue-id>: to=<...>, relay=<...>, ..., status=sent (250 ...)
//	smtp: ... <queue-id>: to=<...>, ..., status=bounced (host said: 550 ...)
//	smtp: ... <queue-id>: to=<...>, ..., status=deferred (connection timed out)
//
// The parser is pure field extraction — no inference, no LLM. Every event keeps
// the exact raw line it came from, so a receipt can cite the literal evidence a
// mail admin would have read by hand. That is the whole point: the log line IS
// the receipt; we just find it and quote it.
package maillog

import (
	"bufio"
	"io"
	"regexp"
	"strings"
	"time"
)

// Status is the delivery disposition Postfix recorded for a recipient.
type Status string

const (
	StatusSent     Status = "sent"     // accepted by the remote MX (250)
	StatusBounced  Status = "bounced"  // hard-rejected (5xx)
	StatusDeferred Status = "deferred" // temporary failure, will retry (4xx/timeout)
	StatusOther    Status = "other"    // a status= we did not specially classify
)

// Event is one recipient-level delivery record, linked to its message-id via the
// queue id. RawLine is the verbatim syslog line for citation.
type Event struct {
	QueueID   string `json:"queue_id"`
	MessageID string `json:"message_id,omitempty"`
	To        string `json:"to"`
	// OrigTo is the original recipient before alias expansion, when Postfix logs it
	// (postfix/local writes "to=<mailbox>, orig_to=<address>"). WO-35: this is the
	// in-log alias bridge — it lets a delivery to an alias-target mailbox correlate
	// to the address the message was actually sent to, with no /etc/aliases parsing.
	OrigTo string `json:"orig_to,omitempty"`
	// MailFrom is the envelope sender (from=<...>), when Postfix logs it. WO-42:
	// the sender is part of the recipient-set correlation key so a forwarded message
	// is never attributed to a different sender's message that happens to share the
	// same recipient set and date.
	MailFrom string    `json:"mail_from,omitempty"`
	Relay    string    `json:"relay,omitempty"`
	Daemon   string    `json:"daemon,omitempty"` // postfix delivery agent: smtp, lmtp, pipe, local, virtual
	Status   Status    `json:"status"`
	Response string    `json:"response,omitempty"` // the remote server's text, e.g. "250 2.0.0 OK"
	Time     time.Time `json:"time,omitempty"`
	TimeRaw  string    `json:"time_raw,omitempty"`
	RawLine  string    `json:"raw_line"`
}

// Log is the parsed result: delivery events plus a queue-id → message-id map
// recovered from cleanup lines (so events that lack an inline message-id can
// still be linked).
type Log struct {
	Events           []Event
	queueToMsg       map[string]string
	queueToFrom      map[string]string // WO-42: queue-id → envelope sender, from the qmgr line.
	coverageFirst    time.Time         // WO-38: earliest timestamp seen in any decoded log line.
	coverageLast     time.Time         // WO-38: latest timestamp seen in any decoded log line.
	coverageFirstRaw string            // WO-38: raw timestamp form used for doctor timestamp diagnostics.
	// seenMsgIDs records every message-id observed in ANY log line (postfix delivery
	// lines AND non-delivery lines such as antivirus/scanner records), lowercased.
	// WO-33: lets a not_found distinguish "no trace at all" from "seen in the log but
	// no delivery event recorded".
	seenMsgIDs map[string]struct{}
}

// SawMessageID reports whether the message-id appeared anywhere in the log, even on
// a non-delivery line (e.g. a scanner entry). It does NOT imply delivery.
func (l Log) SawMessageID(mid string) bool {
	_, ok := l.seenMsgIDs[strings.ToLower(strings.Trim(strings.TrimSpace(mid), "<>"))]
	return ok
}

// TimeRange returns the earliest and latest parsed delivery-event timestamps.
// WO-38: receipts and doctor output need the searched evidence range so a
// not_found result is bounded by what the log actually covered.
func (l Log) TimeRange() (time.Time, time.Time, bool) {
	var first, last time.Time
	for _, ev := range l.Events {
		if ev.Time.IsZero() {
			continue
		}
		if first.IsZero() || ev.Time.Before(first) {
			first = ev.Time
		}
		if last.IsZero() || ev.Time.After(last) {
			last = ev.Time
		}
	}
	if first.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	return first, last, true
}

// CoverageRange returns the earliest and latest timestamps seen in any decoded log line.
func (l Log) CoverageRange() (time.Time, time.Time, bool) {
	if l.coverageFirst.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	return l.coverageFirst, l.coverageLast, true
}

// CoverageTimeRaw returns the raw timestamp text for the earliest covered log line.
func (l Log) CoverageTimeRaw() (string, bool) {
	if l.coverageFirstRaw == "" {
		return "", false
	}
	return l.coverageFirstRaw, true
}

func (l *Log) recordCoverage(tsRaw string, t time.Time) {
	if t.IsZero() {
		return
	}
	if l.coverageFirst.IsZero() || t.Before(l.coverageFirst) {
		l.coverageFirst = t
		l.coverageFirstRaw = tsRaw
	}
	if l.coverageLast.IsZero() || t.After(l.coverageLast) {
		l.coverageLast = t
	}
}

var (
	// The leading syslog timestamp + host + "postfix/<daemon>[pid]: <queue>: rest"
	// We capture the timestamp, the queue id, and the remainder. The timestamp is
	// either the traditional BSD form ("Jun  5 14:09:36") or the RFC3339/ISO-8601
	// form modern rsyslog emits by default ("2026-06-05T14:09:36.750604+02:00").
	lineRe      = regexp.MustCompile(`^(?P<ts>\w{3}\s+\d+\s+\d{2}:\d{2}:\d{2}|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))\s+\S+\s+postfix/(?P<daemon>\w+)\[\d+\]:\s+(?P<qid>[0-9A-F]{6,}):\s+(?P<rest>.*)$`)
	timestampRe = regexp.MustCompile(`^(\w{3}\s+\d+\s+\d{2}:\d{2}:\d{2}|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))\s+`)

	messageIDRe = regexp.MustCompile(`message-id=<?([^>\s,]+)>?`)
	// anyMessageIDRe matches message-id on ANY line, including non-postfix scanner
	// lines that quote it as message-id="<...>" (WO-33). Case-insensitive, tolerant
	// of optional quote and angle brackets.
	anyMessageIDRe = regexp.MustCompile(`(?i)message-id=["']?<?([^>"'\s,]+)>?`)
	toRe           = regexp.MustCompile(`\bto=<([^>]*)>`)
	origToRe       = regexp.MustCompile(`\borig_to=<([^>]*)>`)
	// WO-42: the Postfix qmgr line logs the envelope sender as from=<addr>. The
	// leading \b + "from=" avoids matching orig_to/other tokens.
	mailFromRe = regexp.MustCompile(`\bfrom=<([^>]*)>`)

	// WO-34/WO-40: Dovecot delivers local mail (Postfix mailbox_command=dovecot-lda,
	// or LMTP) and logs under the "dovecot" tag, not postfix/<daemon>. These lines
	// are a LOCAL mailbox handoff -> delivered_local. We match the leading timestamp
	// + host, then a dovecot lda()/lmtp() line whose recipient and msgid we extract,
	// plus a real-world successful-store marker. Observed success markers are
	// "saved mail to <mailbox>" and sieve's "stored mail into mailbox '<mailbox>'".
	// Non-store sieve outcomes such as forwarded or discarded must not match.
	dovecotLineRe  = regexp.MustCompile(`^(?P<ts>\w{3}\s+\d+\s+\d{2}:\d{2}:\d{2}|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))\s+\S+\s+dovecot:\s+(?P<agent>lda|lmtp)\((?P<who>[^)]*)\).*$`)
	dovecotMsgIDRe = regexp.MustCompile(`(?i)msgid=<?([^>\s,]+)>?`)
	dovecotStoreRe = regexp.MustCompile(`(?i)(saved mail to|stored mail into mailbox)\s+'?(.+?)'?\s*$`)
	// lmtpRcptRe pulls a recipient out of the dovecot agent parenthetical when it is
	// an address (LDA: lda(user@dom); LMTP often lmtp(PID, user@dom) or lmtp(PID)).
	emailInTextRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+`)
	relayRe       = regexp.MustCompile(`\brelay=([^,]+)`)
	statusRe      = regexp.MustCompile(`\bstatus=(\w+)\s*(?:\((.*)\))?`)
)

// Traditional BSD syslog timestamps have no year; callers pass the year the log
// covers so "Mon DD HH:MM:SS" can be stamped. RFC3339 timestamps carry their own
// year and zone and ignore the supplied year.
const syslogLayout = "Jan 2 15:04:05 2006"

// parseTimestamp parses either a BSD syslog timestamp ("Jun  5 14:09:36",
// completed with year) or an RFC3339/ISO-8601 timestamp emitted by modern
// rsyslog ("2026-06-05T14:09:36.750604+02:00", self-dating). The bool reports
// whether a time was recovered; callers leave Event.Time zero when false.
func parseTimestamp(tsRaw string, year int) (time.Time, bool) {
	// RFC3339 timestamps start with a 4-digit year and contain a 'T'.
	if len(tsRaw) >= 5 && tsRaw[4] == '-' && strings.Contains(tsRaw, "T") {
		if t, err := time.Parse(time.RFC3339Nano, tsRaw); err == nil {
			return t, true
		}
		if t, err := time.Parse(time.RFC3339, tsRaw); err == nil {
			return t, true
		}
		return time.Time{}, false
	}
	if t, err := time.Parse(syslogLayout, tsRaw+" "+itoa(year)); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func parseLineTimestamp(line string, year int) (string, time.Time, bool) {
	m := timestampRe.FindStringSubmatch(line)
	if m == nil {
		return "", time.Time{}, false
	}
	t, ok := parseTimestamp(m[1], year)
	return m[1], t, ok
}

// parseDovecotLine recognizes a Dovecot LDA/LMTP local mailbox delivery, e.g.:
//
//	... dovecot: lda(auser@example.test): msgid=<id>: saved mail to INBOX
//	... dovecot: lmtp(1234, user@example.test): ... msgid=<id>: ... saved mail to INBOX
//	... dovecot: lda(clerk)<pid><sess>: sieve: msgid=<id>: stored mail into mailbox 'INBOX'
//
// A successful-store line is a LOCAL mailbox handoff -> the event's Daemon is set
// to "dovecot" so the deliver layer classifies it delivered_local (never
// remote/SMTP-2xx). Returns ok=false for any dovecot line that is not a completed
// local store, so deferrals, forwards, discards, and errors are not treated as
// delivery.
func parseDovecotLine(line string, year int) (Event, bool) {
	m := dovecotLineRe.FindStringSubmatch(line)
	if m == nil {
		return Event{}, false
	}
	// WO-40: only successful local-store markers count as Dovecot delivery.
	store := dovecotStoreRe.FindStringSubmatch(line)
	if store == nil {
		return Event{}, false
	}
	tsRaw, who := m[1], m[3]
	marker := strings.ToLower(strings.TrimSpace(store[1]))
	mailbox := strings.TrimSpace(store[2])

	ev := Event{
		Daemon:   "dovecot",
		Status:   StatusSent,
		Relay:    "dovecot",
		Response: marker + " " + mailbox,
		RawLine:  line,
		TimeRaw:  tsRaw,
	}
	// Recipient inside lda(...)/lmtp(...). Dovecot logs either a full address
	// (lda(user@dom)) or a bare mailbox username (lda(clerk)). Prefer a full
	// address; else take the bare username (the leading token before any PID/comma).
	// The deliver layer matches a bare username against the recipient's local-part.
	if addr := emailInTextRe.FindString(who); addr != "" {
		ev.To = strings.ToLower(addr)
	} else if u := strings.TrimSpace(strings.SplitN(who, ",", 2)[0]); u != "" {
		ev.To = strings.ToLower(u)
	}
	if mid := dovecotMsgIDRe.FindStringSubmatch(line); mid != nil {
		ev.MessageID = strings.ToLower(strings.Trim(mid[1], "<>"))
	}
	if t, ok := parseTimestamp(tsRaw, year); ok {
		ev.Time = t
	}
	return ev, true
}

// Parse reads Postfix log lines from r. year is used to complete the
// year-less syslog timestamp (pass the year the log covers; 0 uses 2026 as a
// neutral default for deterministic tests).
func Parse(r io.Reader, year int) Log {
	if year == 0 {
		year = 2026
	}
	l := Log{queueToMsg: map[string]string{}, queueToFrom: map[string]string{}, seenMsgIDs: map[string]struct{}{}}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if tsRaw, t, ok := parseLineTimestamp(line, year); ok {
			l.recordCoverage(tsRaw, t)
		}
		// WO-33: record every message-id seen on ANY line (incl. non-postfix scanner
		// lines) before filtering to postfix delivery lines.
		if mid := anyMessageIDRe.FindStringSubmatch(line); mid != nil {
			l.seenMsgIDs[strings.ToLower(strings.Trim(mid[1], "<>"))] = struct{}{}
		}
		m := lineRe.FindStringSubmatch(line)
		if m == nil {
			// WO-34: not a postfix line — try Dovecot LDA/LMTP local delivery.
			if ev, ok := parseDovecotLine(line, year); ok {
				l.Events = append(l.Events, ev)
			}
			continue
		}
		tsRaw, daemon, qid, rest := m[1], m[2], m[3], m[4]

		// cleanup line: record queue-id -> message-id and move on.
		if mid := messageIDRe.FindStringSubmatch(rest); mid != nil {
			l.queueToMsg[qid] = strings.ToLower(mid[1])
			// A cleanup line carries no to=/status=, so nothing else to do.
			if !strings.Contains(rest, "status=") {
				continue
			}
		}

		// WO-42: the qmgr line ("QID: from=<sender>, size=..., nrcpt=...") carries
		// the envelope sender but no status=. Record queue-id -> sender so delivery
		// events can be backfilled with it for sender-aware set correlation.
		if !strings.Contains(rest, "status=") {
			if fr := mailFromRe.FindStringSubmatch(rest); fr != nil {
				l.queueToFrom[qid] = strings.ToLower(strings.TrimSpace(fr[1]))
			}
		}

		// delivery line: needs a status= to be an Event.
		st := statusRe.FindStringSubmatch(rest)
		if st == nil {
			continue
		}
		ev := Event{
			QueueID: qid,
			Daemon:  strings.ToLower(daemon),
			Status:  classify(st[1]),
			RawLine: line,
			TimeRaw: tsRaw,
		}
		if len(st) > 2 {
			ev.Response = strings.TrimSpace(st[2])
		}
		if to := toRe.FindStringSubmatch(rest); to != nil {
			ev.To = strings.ToLower(strings.TrimSpace(to[1]))
		}
		if ot := origToRe.FindStringSubmatch(rest); ot != nil {
			ev.OrigTo = strings.ToLower(strings.TrimSpace(ot[1]))
		}
		if fr := mailFromRe.FindStringSubmatch(rest); fr != nil {
			ev.MailFrom = strings.ToLower(strings.TrimSpace(fr[1]))
		}
		if rl := relayRe.FindStringSubmatch(rest); rl != nil {
			ev.Relay = strings.TrimSpace(rl[1])
		}
		if t, ok := parseTimestamp(tsRaw, year); ok {
			ev.Time = t
		}
		l.Events = append(l.Events, ev)
	}

	// Backfill message-ids and envelope senders onto events from the queue maps,
	// so a delivery line that lacked an inline message-id or from= inherits them
	// from the cleanup/qmgr lines of the same queue-id (WO-42).
	for i := range l.Events {
		if l.Events[i].MessageID == "" {
			if mid, ok := l.queueToMsg[l.Events[i].QueueID]; ok {
				l.Events[i].MessageID = mid
			}
		}
		if l.Events[i].MailFrom == "" {
			if fr, ok := l.queueToFrom[l.Events[i].QueueID]; ok {
				l.Events[i].MailFrom = fr
			}
		}
	}
	return l
}

// classify maps a postfix status word to our Status.
func classify(s string) Status {
	switch strings.ToLower(s) {
	case "sent":
		return StatusSent
	case "bounced":
		return StatusBounced
	case "deferred":
		return StatusDeferred
	default:
		return StatusOther
	}
}

// EventsForMessageID returns all delivery events whose message-id matches
// (case-insensitive), preserving log order.
func (l Log) EventsForMessageID(mid string) []Event {
	mid = strings.ToLower(strings.Trim(mid, "<>"))
	var out []Event
	for _, e := range l.Events {
		if e.MessageID == mid {
			out = append(out, e)
		}
	}
	return out
}

// EventsForRecipient returns delivery events to a recipient (case-insensitive)
// within an optional time window. If from/to are zero, the window is unbounded.
func (l Log) EventsForRecipient(addr string, from, until time.Time) []Event {
	addr = strings.ToLower(strings.TrimSpace(addr))
	var out []Event
	for _, e := range l.Events {
		if e.To != addr {
			continue
		}
		if !from.IsZero() && e.Time.Before(from) {
			continue
		}
		if !until.IsZero() && !e.Time.IsZero() && e.Time.After(until) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// eventCoversWant reports whether a delivery event's recipient identifies the
// wanted address, by exact match or by a bare Dovecot mailbox username matching
// the wanted local-part (mirrors deliver.recipientMatches; maillog cannot import
// deliver). Both To and OrigTo (the alias bridge) are considered.
func eventCoversWant(e Event, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, evTo := range []string{e.To, e.OrigTo} {
		evTo = strings.ToLower(strings.TrimSpace(evTo))
		if evTo == "" {
			continue
		}
		if evTo == want {
			return true
		}
		if !strings.Contains(evTo, "@") {
			if i := strings.Index(want, "@"); i > 0 && evTo == want[:i] {
				return true
			}
		}
	}
	return false
}

// EventsForRecipientSet finds the delivery events of the single logged message
// whose recipient set covers every address in want, within the window and (when
// known on both sides) sent by wantFrom. WO-42: when the forwarded copy's
// Message-ID does not match the log (Outlook strips it, Exchange rewrites it), the
// full recipient SET plus sender is a strong join key that survives both.
//
// A logged message is grouped by MESSAGE-ID (backfilled onto every event,
// including Dovecot local stores which have no queue-id) so mixed remote+local
// deliveries of one message form one candidate; events with no recoverable
// message-id fall back to their queue-id as the group key so none are dropped.
//
// Sender is part of the uniqueness key: a candidate's MailFrom must equal wantFrom
// when both are known. If wantFrom is set but a candidate has no known sender, that
// candidate is rejected (we cannot confirm the sender). If wantFrom is empty, the
// sender is not constrained. Returns the events of the SINGLE qualifying candidate,
// or nil when zero or more than one qualify (ambiguous — never guess).
func (l Log) EventsForRecipientSet(want []string, wantFrom string, from, until time.Time) []Event {
	if len(want) == 0 {
		return nil
	}
	var wantList []string
	for _, w := range want {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			wantList = append(wantList, w)
		}
	}
	if len(wantList) == 0 {
		return nil
	}
	wantFrom = strings.ToLower(strings.TrimSpace(wantFrom))

	// Group in-window delivery events by message-id (queue-id fallback).
	byMsg := map[string][]Event{}
	for _, e := range l.Events {
		if !from.IsZero() && !e.Time.IsZero() && e.Time.Before(from) {
			continue
		}
		if !until.IsZero() && !e.Time.IsZero() && e.Time.After(until) {
			continue
		}
		key := e.MessageID
		if key == "" {
			key = "qid:" + e.QueueID
		}
		if key == "qid:" {
			continue // no msgid and no queue-id: cannot group this event
		}
		byMsg[key] = append(byMsg[key], e)
	}

	// A candidate qualifies when its events cover every wanted address AND its
	// sender matches (when constrained). Require exactly one qualifying candidate.
	var matchKey string
	matches := 0
	for key, evs := range byMsg {
		if !senderMatches(evs, wantFrom) {
			continue
		}
		covered := true
		for _, w := range wantList {
			any := false
			for _, e := range evs {
				if eventCoversWant(e, w) {
					any = true
					break
				}
			}
			if !any {
				covered = false
				break
			}
		}
		if covered {
			matches++
			matchKey = key
		}
	}
	if matches != 1 {
		return nil
	}
	return byMsg[matchKey]
}

// senderMatches reports whether a candidate message's events are consistent with
// the wanted sender. WO-42: when wantFrom is set, at least one event must carry a
// known MailFrom equal to it, and no event may carry a DIFFERENT known MailFrom.
// A candidate with no known sender at all is rejected when wantFrom is set (we
// cannot confirm it). When wantFrom is empty, the sender is unconstrained.
func senderMatches(evs []Event, wantFrom string) bool {
	if wantFrom == "" {
		return true
	}
	sawMatch := false
	for _, e := range evs {
		mf := strings.ToLower(strings.TrimSpace(e.MailFrom))
		if mf == "" {
			continue
		}
		if mf != wantFrom {
			return false
		}
		sawMatch = true
	}
	return sawMatch
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
