// Package eml extracts the lookup key from a dropped email: the Message-ID and
// recipients that index it into the mail log.
//
// It accepts a real RFC822 message (headers then a blank line then the body) and
// also tolerates a pasted top-of-thread block ("From:/Sent:/To:/Subject:")
// without a real Message-ID — in which case Message-ID is empty and the
// correlator falls back to recipient + time matching. No body interpretation,
// no LLM: this only reads addressing headers.
package eml

import (
	"bufio"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
)

// Email is the addressing metadata we use to find delivery events.
type Email struct {
	MessageID string    `json:"message_id,omitempty"`
	From      string    `json:"from,omitempty"`
	Sender    string    `json:"sender,omitempty"` // WO-13: ownership header for forwarded sent-message receipts
	To        []string  `json:"to,omitempty"`
	Cc        []string  `json:"cc,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	Date      time.Time `json:"date,omitempty"`
	DateRaw   string    `json:"date_raw,omitempty"`
}

var lenientDateLayouts = []string{
	"Monday, January 2, 2006 3:04 PM",
	"Monday, January 2, 2006 3:04:05 PM",
}

var subjectWordDecoder = &mime.WordDecoder{CharsetReader: subjectCharsetReader}

var (
	headerSoftBreakRe           = regexp.MustCompile(`=\r?\n[ \t]+`)
	qpAddressEscapeRe           = regexp.MustCompile(`(?i)=(0D|0A|09|20|22|27|2C|3B|3C|3D|3E|40)`)
	qpAddressStructuralEscapeRe = regexp.MustCompile(`(?i)=(0D|0A|09|20|22|27|2C|3B|3C|3D|3E)`)
	addrSpecRe                  = regexp.MustCompile(`[A-Za-z0-9.!#$%&*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)+`)
)

const qpEscapeLength = 3

// Recipients returns To + Cc addresses, lowercased and de-duplicated.
func (e Email) Recipients() []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range append(append([]string{}, e.To...), e.Cc...) {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// Parse reads a dropped email and extracts its addressing metadata. It first
// tries strict RFC822 parsing; if that yields no usable headers it falls back to
// a lenient line scan for pasted threads.
func Parse(r io.Reader) (Email, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return Email{}, err
	}
	raw = repairHeaderSoftBreaks(raw)
	if e, ok := parseRFC822(raw); ok {
		return e, nil
	}
	return parseLenient(raw), nil
}

func parseRFC822(raw []byte) (Email, bool) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return Email{}, false
	}
	h := msg.Header
	// WO-8: mail.ReadMessage can accept pasted Outlook-style blocks, so honor
	// Sent here before recipient fallback depends on the parsed send time.
	dateRaw := h.Get("Date")
	if dateRaw == "" {
		dateRaw = h.Get("Sent")
	}
	e := Email{
		MessageID: normalizeMessageID(h.Get("Message-Id")),
		From:      h.Get("From"),
		Sender:    h.Get("Sender"),
		Subject:   decodeSubjectHeader(h.Get("Subject")),
		To:        splitAddrs(h.Get("To")),
		Cc:        splitAddrs(h.Get("Cc")),
		DateRaw:   dateRaw,
	}
	if t, ok := parseLenientDate(dateRaw); ok {
		e.Date = t
	}
	// Consider it a real RFC822 parse only if we got at least a Message-ID or a
	// recipient — otherwise fall back to the lenient scanner.
	if e.MessageID == "" && len(e.To) == 0 {
		return Email{}, false
	}
	return e, true
}

// parseLenient scans line-by-line for the first header block, for pasted threads
// that are not valid RFC822.
func parseLenient(raw []byte) Email {
	var e Email
	var key, value string
	flush := func() {
		if key == "" {
			return
		}
		switch key {
		case "message-id":
			e.MessageID = normalizeMessageID(value)
		case "from":
			if e.From == "" {
				e.From = value
			}
		case "sender":
			if e.Sender == "" {
				e.Sender = value
			}
		case "to":
			if len(e.To) == 0 {
				e.To = splitAddrs(value)
			}
		case "cc":
			if len(e.Cc) == 0 {
				e.Cc = splitAddrs(value)
			}
		case "subject":
			if e.Subject == "" {
				e.Subject = decodeSubjectHeader(value)
			}
		case "sent", "date":
			if e.DateRaw == "" {
				e.DateRaw = value
				if t, ok := parseLenientDate(e.DateRaw); ok {
					e.Date = t
				}
			}
		}
		key, value = "", ""
	}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		ln := sc.Text()
		if strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t") {
			if key != "" {
				value += " " + strings.TrimSpace(ln)
			}
			continue
		}
		flush()
		lower := strings.ToLower(ln)
		switch {
		case strings.HasPrefix(lower, "message-id:"):
			key, value = "message-id", after(ln, ":")
		case strings.HasPrefix(lower, "from:") && e.From == "":
			key, value = "from", after(ln, ":")
		case strings.HasPrefix(lower, "sender:") && e.Sender == "":
			key, value = "sender", after(ln, ":")
		case strings.HasPrefix(lower, "to:") && len(e.To) == 0:
			key, value = "to", after(ln, ":")
		case strings.HasPrefix(lower, "cc:") && len(e.Cc) == 0:
			key, value = "cc", after(ln, ":")
		case strings.HasPrefix(lower, "subject:") && e.Subject == "":
			key, value = "subject", after(ln, ":")
		case (strings.HasPrefix(lower, "sent:") || strings.HasPrefix(lower, "date:")) && e.DateRaw == "":
			key, value = strings.TrimSuffix(strings.ToLower(ln[:strings.Index(ln, ":")]), ":"), after(ln, ":")
		}
	}
	flush()
	return e
}

// decodeSubjectHeader decodes RFC 2047 encoded-words for readable receipts.
// WO-25: non-UTF-8 Russian mail subjects need explicit deterministic charset readers.
func decodeSubjectHeader(raw string) string {
	if raw == "" {
		return ""
	}
	decoded, err := subjectWordDecoder.DecodeHeader(raw)
	if err != nil {
		return raw
	}
	return decoded
}

func subjectCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "koi8-r", "koi8r":
		return charmap.KOI8R.NewDecoder().Reader(input), nil
	case "windows-1251", "cp1251", "x-cp1251":
		return charmap.Windows1251.NewDecoder().Reader(input), nil
	default:
		return nil, fmt.Errorf("unsupported subject charset %q", charset)
	}
}

func parseLenientDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := mail.ParseDate(raw); err == nil {
		return t, true
	}
	// WO-8: pasted top-of-thread dates need explicit layouts so recipient
	// fallback can stay bounded instead of guessing across the whole log.
	for _, layout := range lenientDateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// normalizeMessageID strips angle brackets and lowercases for stable matching.
func normalizeMessageID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "<>")
	return strings.ToLower(s)
}

// WO-37: Outlook-style forwards sometimes put quoted-printable soft breaks in
// address headers. They are line-wrapping artifacts, not address bytes.
func repairHeaderSoftBreaks(raw []byte) []byte {
	return headerSoftBreakRe.ReplaceAll(raw, nil)
}

// WO-37: recover malformed Outlook address headers before falling back to token
// extraction: collapse semicolon separators and remove mailto: URL wrappers
// without treating them as distinct recipients.
func normalizeAddressHeader(v string, decodeQP bool) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if decodeQP {
		decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(v)))
		if err == nil && len(decoded) > 0 {
			v = string(decoded)
		}
	}
	v = strings.ReplaceAll(v, ";", ",")
	v = strings.ReplaceAll(v, "mailto:", "")
	v = strings.ReplaceAll(v, "MAILTO:", "")
	v = strings.ReplaceAll(v, "< <", "<")
	v = strings.ReplaceAll(v, "> >", ">")
	return strings.TrimSpace(v)
}

func shouldDecodeAddressHeader(v string) bool {
	return hasStructuralAddressQPEscape(v) || qpAddressEscapeRe.MatchString(v)
}

func hasStructuralAddressQPEscape(v string) bool {
	return strings.Contains(v, "=\r\n") || strings.Contains(v, "=\n") || qpAddressStructuralEscapeRe.MatchString(v)
}

// splitAddrs extracts bare email addresses from a header value, preferring
// RFC822 address parsing and falling back to a token scan for messy pasted
// values like: 'jdoe@x.test' <jdoe@x.test>
func splitAddrs(v string) []string {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return nil
	}
	// WO-37: first parse without QP decoding. Valid local-parts may contain
	// "=HH"; decoding them would manufacture a different address.
	normalized := normalizeAddressHeader(raw, false)
	if out := parseStrictAddressList(normalized); len(out) > 0 {
		return out
	}
	if shouldDecodeAddressHeader(raw) {
		// WO-37: retry strict parsing after QP repair before regex fallback can
		// misread encoded delimiters such as =3Caddr@example.test=3E.
		decoded := normalizeAddressHeader(raw, true)
		if out := parseStrictAddressList(decoded); len(out) > 0 {
			return out
		}
		if out := scanAddressTokens(normalized); len(out) > 0 {
			return out
		}
		if out := scanAddressTokens(decoded); len(out) > 0 {
			return out
		}
	}
	return scanAddressTokens(normalized)
}

func parseStrictAddressList(v string) []string {
	if v == "" {
		return nil
	}
	if addrs, err := mail.ParseAddressList(v); err == nil && len(addrs) > 0 {
		out := make([]string, 0, len(addrs))
		for _, a := range addrs {
			addr := strings.ToLower(strings.TrimSpace(a.Address))
			if addrSpecRe.FindString(addr) != addr {
				out = nil
				break
			}
			out = append(out, addr)
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func scanAddressTokens(v string) []string {
	if v == "" {
		return nil
	}
	// Fallback: pull anything that looks like an address out of the string.
	var out []string
	seen := map[string]bool{}
	for _, loc := range addrSpecRe.FindAllStringIndex(v, -1) {
		match := v[loc[0]:loc[1]]
		if isEncodedAddressDelimiterArtifact(v, loc[0], loc[1]) {
			continue
		}
		t := strings.ToLower(strings.TrimSpace(match))
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// WO-37: skip the raw token only when it is visibly the left half of an encoded
// angle-delimiter pair, not just because a valid local-part begins with =HH.
func isEncodedAddressDelimiterArtifact(v string, start, end int) bool {
	if start < 0 || end > len(v) || start >= end {
		return false
	}
	if start > 0 && v[start-1] == '<' {
		return false
	}
	if !startsWithStructuralAddressQPEscape(v[start:end]) {
		return false
	}
	if len(v[end:]) < qpEscapeLength {
		return false
	}
	return strings.EqualFold(v[end:end+qpEscapeLength], "=3e")
}

func startsWithStructuralAddressQPEscape(v string) bool {
	if len(v) < qpEscapeLength || v[0] != '=' {
		return false
	}
	return strings.EqualFold(v[:qpEscapeLength], "=3c")
}

func after(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return strings.TrimSpace(s[i+len(sep):])
	}
	return strings.TrimSpace(s)
}
