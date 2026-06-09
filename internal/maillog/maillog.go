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
	QueueID   string    `json:"queue_id"`
	MessageID string    `json:"message_id,omitempty"`
	To        string    `json:"to"`
	Relay     string    `json:"relay,omitempty"`
	Daemon    string    `json:"daemon,omitempty"` // postfix delivery agent: smtp, lmtp, pipe, local, virtual
	Status    Status    `json:"status"`
	Response  string    `json:"response,omitempty"` // the remote server's text, e.g. "250 2.0.0 OK"
	Time      time.Time `json:"time,omitempty"`
	TimeRaw   string    `json:"time_raw,omitempty"`
	RawLine   string    `json:"raw_line"`
}

// Log is the parsed result: delivery events plus a queue-id → message-id map
// recovered from cleanup lines (so events that lack an inline message-id can
// still be linked).
type Log struct {
	Events     []Event
	queueToMsg map[string]string
}

var (
	// The leading syslog timestamp + host + "postfix/<daemon>[pid]: <queue>: rest"
	// We capture the timestamp, the queue id, and the remainder. The timestamp is
	// either the traditional BSD form ("Jun  5 14:09:36") or the RFC3339/ISO-8601
	// form modern rsyslog emits by default ("2026-06-05T14:09:36.750604+02:00").
	lineRe = regexp.MustCompile(`^(?P<ts>\w{3}\s+\d+\s+\d{2}:\d{2}:\d{2}|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))\s+\S+\s+postfix/(?P<daemon>\w+)\[\d+\]:\s+(?P<qid>[0-9A-F]{6,}):\s+(?P<rest>.*)$`)

	messageIDRe = regexp.MustCompile(`message-id=<?([^>\s,]+)>?`)
	toRe        = regexp.MustCompile(`\bto=<([^>]*)>`)
	relayRe     = regexp.MustCompile(`\brelay=([^,]+)`)
	statusRe    = regexp.MustCompile(`\bstatus=(\w+)\s*(?:\((.*)\))?`)
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

// Parse reads Postfix log lines from r. year is used to complete the
// year-less syslog timestamp (pass the year the log covers; 0 uses 2026 as a
// neutral default for deterministic tests).
func Parse(r io.Reader, year int) Log {
	if year == 0 {
		year = 2026
	}
	l := Log{queueToMsg: map[string]string{}}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		m := lineRe.FindStringSubmatch(line)
		if m == nil {
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
		if rl := relayRe.FindStringSubmatch(rest); rl != nil {
			ev.Relay = strings.TrimSpace(rl[1])
		}
		if t, ok := parseTimestamp(tsRaw, year); ok {
			ev.Time = t
		}
		l.Events = append(l.Events, ev)
	}

	// Backfill message-ids onto events from the queue map.
	for i := range l.Events {
		if l.Events[i].MessageID == "" {
			if mid, ok := l.queueToMsg[l.Events[i].QueueID]; ok {
				l.Events[i].MessageID = mid
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
