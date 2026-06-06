// Package thread parses a pasted or forwarded email thread into an ordered
// sequence of messages. It is deliberately tolerant: real threads arrive as
// quoted-reply blocks ("From: / Sent: / Subject:" headers inlined into the body),
// not clean RFC822, so the parser splits on those forwarded-header boundaries.
//
// The parser extracts only structure (who, when, what was said) — it makes no
// judgement about status or deadlines. That is the extractor's and status
// engine's job, and keeping parsing dumb keeps the provenance honest: a message
// id points at a real span of the input text.
package thread

import (
	"bufio"
	"regexp"
	"strings"
	"time"
)

// Message is one email in a thread. Fields are best-effort: a forwarded block
// may omit some headers. Body is the text between this message's header block
// and the next message boundary.
type Message struct {
	// ID is a stable, position-derived identifier ("msg-1" is the most recent
	// message, increasing into the past) so citations are human-readable.
	ID string `json:"id"`
	// From is the raw sender string as it appeared (name and/or address).
	From string `json:"from"`
	// To is the raw recipient string.
	To string `json:"to,omitempty"`
	// Cc is the raw cc string.
	Cc string `json:"cc,omitempty"`
	// Subject as it appeared on this message.
	Subject string `json:"subject,omitempty"`
	// Date is the parsed send time, or the zero value if it could not be parsed.
	Date time.Time `json:"date,omitempty"`
	// DateRaw is the original date string, kept for citation even when parsing
	// fails.
	DateRaw string `json:"date_raw,omitempty"`
	// Body is the message text (excludes this message's own header block and any
	// deeper quoted messages).
	Body string `json:"body"`
}

// Thread is the ordered set of messages parsed from one input. Index 0 is the
// most recent message (the top of a typical reply chain).
type Thread struct {
	Messages []Message `json:"messages"`
}

// Each forwarded/quoted message in a pasted thread begins with a header block.
// We anchor on a "From:" line that is followed (within a few lines) by a "Sent:"
// or "Date:" line — the signature of an inlined forwarded header, as opposed to
// a "From:" that merely appears in prose.
var (
	fromLine    = regexp.MustCompile(`(?mi)^\s*From:\s*(.+?)\s*$`)
	sentLine    = regexp.MustCompile(`(?mi)^\s*(?:Sent|Date):\s*(.+?)\s*$`)
	toLine      = regexp.MustCompile(`(?mi)^\s*To:\s*(.+?)\s*$`)
	ccLine      = regexp.MustCompile(`(?mi)^\s*Cc:\s*(.+?)\s*$`)
	subjectLine = regexp.MustCompile(`(?mi)^\s*Subject:\s*(.+?)\s*$`)
)

// dateLayouts are the formats we attempt, in order. Email clients are
// inconsistent; we try the common ones and fall back to keeping the raw string.
var dateLayouts = []string{
	"Monday, January 2, 2006 3:04 PM",
	"Monday, January 2, 2006 3:04:05 PM",
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"2 January 2006 15:04",
	"January 2, 2006 3:04 PM",
	"1/2/2006 3:04 PM",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseDate tries each known layout and returns the first that succeeds.
func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// headerBoundary describes where a forwarded message's header block starts.
type headerBoundary struct {
	start int // line index of the "From:" line
}

// Parse splits raw thread text into messages. The first message (msg-1) is the
// top-of-thread content (the most recent author's text), and each subsequent
// forwarded block becomes the next message going back in time.
func Parse(raw string) Thread {
	lines := splitLines(raw)

	// Find every forwarded-header boundary: a "From:" line with a "Sent:"/"Date:"
	// line within the next few lines.
	var bounds []headerBoundary
	for i, ln := range lines {
		if !fromLine.MatchString(ln) {
			continue
		}
		// Look ahead a small window for a Sent/Date line to confirm this is a
		// header block, not prose containing "From:".
		if hasSentWithin(lines, i+1, 4) {
			bounds = append(bounds, headerBoundary{start: i})
		}
	}

	var msgs []Message

	// The text before the first boundary is the top message (the latest reply,
	// which has no inlined header of its own in a typical paste).
	if len(bounds) == 0 || bounds[0].start > 0 {
		end := len(lines)
		if len(bounds) > 0 {
			end = bounds[0].start
		}
		top := strings.TrimSpace(joinLines(lines[:end]))
		if top != "" {
			msgs = append(msgs, Message{Body: top})
		}
	}

	// Each boundary begins a forwarded message that runs until the next boundary.
	for bi, b := range bounds {
		end := len(lines)
		if bi+1 < len(bounds) {
			end = bounds[bi+1].start
		}
		block := lines[b.start:end]
		msgs = append(msgs, parseBlock(block))
	}

	// Assign ids in thread order (msg-1 newest).
	for i := range msgs {
		msgs[i].ID = "msg-" + itoa(i+1)
	}
	return Thread{Messages: msgs}
}

// parseBlock turns one forwarded block (starting at its From: line) into a
// Message, separating the header lines from the body.
func parseBlock(block []string) Message {
	var m Message
	bodyStart := 0
	// The header block is the contiguous run of recognized header lines at the
	// top; the body is everything after the first blank line following them.
	for i, ln := range block {
		switch {
		case fromLine.MatchString(ln):
			m.From = firstSubmatch(fromLine, ln)
		case sentLine.MatchString(ln):
			m.DateRaw = firstSubmatch(sentLine, ln)
			if t, ok := parseDate(m.DateRaw); ok {
				m.Date = t
			}
		case toLine.MatchString(ln):
			m.To = firstSubmatch(toLine, ln)
		case ccLine.MatchString(ln):
			m.Cc = firstSubmatch(ccLine, ln)
		case subjectLine.MatchString(ln):
			m.Subject = firstSubmatch(subjectLine, ln)
			// Subject is conventionally the last header line; body follows.
			bodyStart = i + 1
		}
	}
	if bodyStart < len(block) {
		m.Body = strings.TrimSpace(joinLines(block[bodyStart:]))
	}
	return m
}

// hasSentWithin reports whether a Sent/Date header appears within n lines from
// index start.
func hasSentWithin(lines []string, start, n int) bool {
	for i := start; i < len(lines) && i < start+n; i++ {
		if sentLine.MatchString(lines[i]) {
			return true
		}
	}
	return false
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func splitLines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") }

// itoa is a tiny dependency-free int->string for small positive ids.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
