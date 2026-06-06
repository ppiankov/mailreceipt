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
	"io"
	"net/mail"
	"strings"
	"time"
)

// Email is the addressing metadata we use to find delivery events.
type Email struct {
	MessageID string    `json:"message_id,omitempty"`
	From      string    `json:"from,omitempty"`
	To        []string  `json:"to,omitempty"`
	Cc        []string  `json:"cc,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	Date      time.Time `json:"date,omitempty"`
	DateRaw   string    `json:"date_raw,omitempty"`
}

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
	e := Email{
		MessageID: normalizeMessageID(h.Get("Message-Id")),
		From:      h.Get("From"),
		Subject:   h.Get("Subject"),
		To:        splitAddrs(h.Get("To")),
		Cc:        splitAddrs(h.Get("Cc")),
		DateRaw:   h.Get("Date"),
	}
	if t, err := mail.ParseDate(h.Get("Date")); err == nil {
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
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		ln := sc.Text()
		lower := strings.ToLower(ln)
		switch {
		case strings.HasPrefix(lower, "message-id:"):
			e.MessageID = normalizeMessageID(after(ln, ":"))
		case strings.HasPrefix(lower, "from:") && e.From == "":
			e.From = after(ln, ":")
		case strings.HasPrefix(lower, "to:") && len(e.To) == 0:
			e.To = splitAddrs(after(ln, ":"))
		case strings.HasPrefix(lower, "cc:") && len(e.Cc) == 0:
			e.Cc = splitAddrs(after(ln, ":"))
		case strings.HasPrefix(lower, "subject:") && e.Subject == "":
			e.Subject = after(ln, ":")
		case (strings.HasPrefix(lower, "sent:") || strings.HasPrefix(lower, "date:")) && e.DateRaw == "":
			e.DateRaw = after(ln, ":")
		}
	}
	return e
}

// normalizeMessageID strips angle brackets and lowercases for stable matching.
func normalizeMessageID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "<>")
	return strings.ToLower(s)
}

// splitAddrs extracts bare email addresses from a header value, preferring
// RFC822 address parsing and falling back to a token scan for messy pasted
// values like: 'jdoe@x.test' <jdoe@x.test>
func splitAddrs(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if addrs, err := mail.ParseAddressList(v); err == nil && len(addrs) > 0 {
		out := make([]string, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, strings.ToLower(a.Address))
		}
		return out
	}
	// Fallback: pull anything that looks like an address out of the string.
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '<' || r == '>' || r == '\'' || r == '"'
	}) {
		if strings.Contains(tok, "@") {
			t := strings.ToLower(tok)
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

func after(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return strings.TrimSpace(s[i+len(sep):])
	}
	return strings.TrimSpace(s)
}
