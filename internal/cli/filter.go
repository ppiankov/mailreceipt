package cli

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ppiankov/mailreceipt/internal/config"
	"github.com/ppiankov/mailreceipt/internal/deliver"
	"github.com/ppiankov/mailreceipt/internal/eml"
	"github.com/ppiankov/mailreceipt/internal/maillog"
	"github.com/ppiankov/mailreceipt/internal/receipt"
	"github.com/spf13/cobra"
)

const (
	filterLegacyReplyBoundary = "mailreceipt-filter-reply"      // WO-22: old static boundary retained only as a safety sentinel.
	filterReplyBoundaryPrefix = filterLegacyReplyBoundary + "-" // WO-22: generated boundaries stay recognizable without being predictable.
	filterBoundaryRandomBytes = 16                              // WO-22: 128 bits makes adversarial delimiter guesses impractical.
	filterBoundaryMaxAttempts = 8                               // WO-22: fail closed if injected/random candidates keep colliding.
	filterBoundaryMaxLength   = 70                              // WO-22: RFC 2046 boundary text length limit.
	filterBase64LineLength    = 76                              // WO-19: MIME base64 bodies are wrapped for transport safety.
	safeMailboxLocalChars     = "!#$%&'*+-/=?^_`{|}~"           // WO-21: dot-atom symbols allowed in trusted local-parts.
)

func filterCmd() *cobra.Command {
	var (
		logPath      string
		logYear      int
		caseRef      string
		envelopeFrom string
		replyFrom    string
		dedupDir     string
	)
	cmd := &cobra.Command{
		Use:   "filter",
		Short: "Read a forwarded sent email on stdin and write an automatic receipt reply",
		Long: "Reads a trigger email on stdin, extracts an attached sent message, and writes\n" +
			"a reply email containing a cited delivery receipt when the request is authorized.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, _ := config.Load(config.FileName)
			if logPath == "" && !cmd.Flags().Changed("log") {
				logPath = cfg.Log
			}
			if !cmd.Flags().Changed("log-year") && cfg.LogYear != 0 {
				logYear = cfg.LogYear
			}
			if cfg.CasePrefix != "" {
				caseRef = cfg.CasePrefix + caseRef
			}
			if replyFrom == "" && !cmd.Flags().Changed("from") {
				replyFrom = cfg.ReceiptFilter.ReplyFrom
			}
			if dedupDir == "" && !cmd.Flags().Changed("dedup-dir") {
				dedupDir = cfg.ReceiptFilter.DedupDir
			}

			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return filterRefuse(cmd, "could not read trigger message: %v", err)
			}
			if reason := filterLoopGuardReason(raw); reason != "" {
				return filterRefuse(cmd, reason)
			}
			// WO-32: suppress a duplicate receipt when Postfix re-delivers the same
			// trigger to the pipe. Keyed on the trigger Message-ID; opt-in.
			if !claimTrigger(dedupDir, raw) {
				return filterRefuse(cmd, "duplicate trigger suppressed by --dedup-dir")
			}

			// WO-23: envelope sender is the MTA-authenticated trust boundary.
			if strings.TrimSpace(envelopeFrom) == "" {
				return filterRefuse(cmd, "missing authenticated envelope sender")
			}
			triggerSender, ok := normalizeTrustedAddress(envelopeFrom)
			if !ok {
				return filterRefuse(cmd, "invalid authenticated envelope sender")
			}
			if !domainAllowed(triggerSender, cfg.ReceiptFilter.Domains) {
				return filterRefuse(cmd, "envelope sender is not authorized for configured receipt domains")
			}
			if replyFrom == "" {
				// WO-16: keep generated replies RFC5322-complete even without explicit config.
				replyFrom = "mailreceipt@" + addressDomain(triggerSender)
			} else {
				var ok bool
				// WO-20: configured reply identity is trusted input, not a messy header.
				replyFrom, ok = normalizeTrustedAddress(replyFrom)
				if !ok {
					return filterRefuse(cmd, "invalid receipt reply sender")
				}
			}
			if !domainAllowed(replyFrom, cfg.ReceiptFilter.Domains) {
				return filterRefuse(cmd, "receipt reply sender is not authorized for configured receipt domains")
			}
			forwarded, err := eml.ExtractForwardedEmail(raw)
			if err != nil {
				return filterRefuse(cmd, "could not extract forwarded message: %v", err)
			}
			if len(forwarded.Email.Recipients()) == 0 {
				if forwarded.Attached {
					return filterRefuse(cmd, "forwarded message attachment has no parsed recipients")
				}
				return filterRefuse(cmd, "no message/rfc822 attachment or parseable forwarded recipients found")
			}
			if !sharesFilterTeam(cfg.ReceiptFilter, triggerSender, forwarded.Email) {
				return filterRefuse(cmd, "envelope sender is not authorized for forwarded message owner")
			}
			if logPath == "" {
				return filterRefuse(cmd, "missing --log path")
			}
			logInput, err := readLogInput(logPath)
			if err != nil {
				return filterRefuse(cmd, "could not read log input: %v", err)
			}
			log := maillog.Parse(bytes.NewReader(logInput.Data), logYear)

			e := forwarded.Email
			if forwarded.Attached && e.MessageID != "" {
				// WO-13: an attached .eml with a Message-ID is a precise selector;
				// require exact Message-ID correlation and never borrow recipient-
				// window lines. WO-41: Outlook's forward-as-attachment STRIPS the
				// original Message-ID (keeping only its Date), so an attachment with
				// no Message-ID has no exact key. In that case keep the Date so the
				// recipient+date-window fallback can run — it attributes only on a
				// unique (single queue-id) match, so it still never guesses.
				e.Date = time.Time{}
			}
			res := deliver.Analyze(e, log)
			rec := receipt.New(res, caseRef, time.Time{})
			annotateReceiptLogRange(&rec, log)
			return writeFilterReply(cmd.OutOrStdout(), triggerSender, replyFrom, filterNow(), rec)
		},
	}
	cmd.Flags().StringVar(&envelopeFrom, "envelope-from", "", "MTA-authenticated envelope sender for the trigger email")
	cmd.Flags().StringVar(&replyFrom, "from", "", "From address for generated receipt replies")
	cmd.Flags().StringVar(&logPath, "log", "", "path, comma-list, or glob of Postfix mail logs")
	cmd.Flags().StringVar(&caseRef, "case", "", "case/matter reference to stamp on the receipt")
	cmd.Flags().IntVar(&logYear, "log-year", 0, "year for the year-less syslog timestamps (default 2026)")
	cmd.Flags().StringVar(&dedupDir, "dedup-dir", "", "directory for trigger idempotency state (suppresses duplicate receipts on pipe re-delivery)")
	return cmd
}

type logInput struct {
	Data  []byte
	Paths []string
}

// WO-38: --log accepts a single path, a comma/list-separated path set, or a glob;
// .gz rotated logs are decoded before parsing.
func readLogInput(spec string) (logInput, error) {
	paths, err := expandLogPaths(spec)
	if err != nil {
		return logInput{}, err
	}
	var buf bytes.Buffer
	for _, path := range paths {
		data, err := readLogPath(path)
		if err != nil {
			return logInput{}, err
		}
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			buf.WriteByte('\n')
		}
		buf.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	return logInput{Data: buf.Bytes(), Paths: paths}, nil
}

func expandLogPaths(spec string) ([]string, error) {
	var parts []string
	if strings.Contains(spec, ",") {
		parts = strings.Split(spec, ",")
	} else if split := filepath.SplitList(spec); len(split) > 1 {
		parts = split
	} else {
		parts = []string{spec}
	}
	var paths []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.ContainsAny(part, "*?[") {
			matches, err := filepath.Glob(part)
			if err != nil {
				return nil, err
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("log pattern %q matched no files", part)
			}
			sort.Strings(matches)
			paths = append(paths, matches...)
			continue
		}
		paths = append(paths, part)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no log paths supplied")
	}
	return paths, nil
}

func readLogPath(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	var gz *gzip.Reader
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gz, err = gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	return io.ReadAll(r)
}

// WO-38: include the searched log coverage range in generated receipts.
func annotateReceiptLogRange(rec *receipt.Receipt, log maillog.Log) {
	rec.Result.Caveat = strings.TrimSpace(rec.Result.Caveat + " " + logRangeSentence(log))
}

func logRangeSentence(log maillog.Log) string {
	first, last, ok := log.CoverageRange()
	if !ok {
		return "Searched log time range: no parsed log timestamps."
	}
	if first.Equal(last) {
		return "Searched log time range: " + formatLogTime(first) + "."
	}
	return "Searched log time range: " + formatLogTime(first) + " to " + formatLogTime(last) + "."
}

func formatLogTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05 -0700")
}

// WO-16: injectable clock keeps reply Date tests deterministic.
var filterNow = time.Now

// WO-22: injectable MIME boundary source keeps random-boundary tests deterministic.
var filterNewBoundary = randomFilterReplyBoundary

func filterLoopGuardReason(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "trigger message is not parseable RFC822"
	}
	autoSubmitted := strings.TrimSpace(msg.Header.Get("Auto-Submitted"))
	if autoSubmitted != "" && !strings.EqualFold(autoSubmitted, "no") {
		return "auto-submitted trigger suppressed"
	}
	if strings.EqualFold(strings.TrimSpace(msg.Header.Get("Precedence")), "bulk") {
		return "bulk trigger suppressed"
	}
	return ""
}

func filterRefuse(cmd *cobra.Command, format string, args ...any) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "mailreceipt filter: "+format+"\n", args...)
	return nil
}

func sharesFilterTeam(cfg config.ReceiptFilterConfig, triggerSender string, sent eml.Email) bool {
	owners := filterOwnerAddresses(sent)
	if triggerSender == "" || len(owners) == 0 {
		return false
	}
	if len(cfg.Teams) == 0 {
		for _, ownerAddr := range owners {
			if domainAllowed(ownerAddr, cfg.Domains) && addressDomain(triggerSender) == addressDomain(ownerAddr) {
				return true
			}
		}
		return false
	}
	for _, members := range cfg.Teams {
		triggerInTeam := false
		ownerInTeam := map[string]bool{}
		for _, m := range members {
			addr, ok := normalizeTrustedAddress(m)
			if !ok {
				continue
			}
			if addr == triggerSender {
				triggerInTeam = true
			}
			for _, ownerAddr := range owners {
				if domainAllowed(ownerAddr, cfg.Domains) && addr == ownerAddr {
					ownerInTeam[ownerAddr] = true
				}
			}
		}
		if triggerInTeam && len(ownerInTeam) > 0 {
			return true
		}
	}
	return false
}

func filterOwnerAddresses(sent eml.Email) []string {
	// WO-17: From and Sender are independent ownership candidates.
	seen := map[string]bool{}
	var owners []string
	for _, candidate := range []string{sent.From, sent.Sender} {
		addr := normalizeAddress(candidate)
		if addr != "" && !seen[addr] {
			seen[addr] = true
			owners = append(owners, addr)
		}
	}
	return owners
}

func domainAllowed(addr string, domains []string) bool {
	domain := addressDomain(addr)
	if domain == "" {
		return false
	}
	for _, d := range domains {
		if domain == strings.ToLower(strings.TrimSpace(d)) {
			return true
		}
	}
	return false
}

func addressDomain(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i+1 < len(addr) {
		return strings.ToLower(strings.TrimSpace(addr[i+1:]))
	}
	return ""
}

func normalizeAddress(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if addr, ok := normalizeTrustedAddress(v); ok {
		return addr
	}
	if addrs, err := mail.ParseAddressList(v); err == nil {
		for _, addr := range addrs {
			if normalized, ok := normalizeMailboxAddress(addr.Address); ok {
				return normalized
			}
		}
	}
	for _, tok := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '<' || r == '>' || r == '\'' || r == '"'
	}) {
		if addr, ok := normalizeTrustedAddress(tok); ok {
			return addr
		}
	}
	return ""
}

// WO-20: trusted sender identities must be valid single mailbox addresses.
func normalizeTrustedAddress(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	addrs, err := mail.ParseAddressList(v)
	if err != nil || len(addrs) != 1 {
		return "", false
	}
	return normalizeMailboxAddress(addrs[0].Address)
}

func normalizeMailboxAddress(addr string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(addr))
	at := strings.Index(normalized, "@")
	if at <= 0 || at != strings.LastIndex(normalized, "@") || at == len(normalized)-1 {
		return "", false
	}
	if !isSafeMailboxLocal(normalized[:at]) || !isSafeMailboxDomain(normalized[at+1:]) {
		return "", false
	}
	return normalized, true
}

// WO-21: trusted identities must be safe to write as simple mailbox headers.
func isSafeMailboxLocal(local string) bool {
	if local == "" || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return false
	}
	for _, r := range local {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '.' || strings.ContainsRune(safeMailboxLocalChars, r) {
			continue
		}
		return false
	}
	return true
}

// WO-21: trusted domains are limited to simple DNS labels before raw header use.
func isSafeMailboxDomain(domain string) bool {
	if domain == "" || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if r >= 'a' && r <= 'z' {
				continue
			}
			if r >= '0' && r <= '9' {
				continue
			}
			if r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func writeFilterReply(w io.Writer, to, from string, now time.Time, rec receipt.Receipt) error {
	jsonBody, err := rec.JSON()
	if err != nil {
		return err
	}
	textBody, err := encodeQuotedPrintable(rec.PlainText())
	if err != nil {
		return err
	}
	encodedJSONBody := base64.StdEncoding.EncodeToString(jsonBody)
	boundary, err := filterReplyBoundary(textBody, encodedJSONBody)
	if err != nil {
		return err
	}
	subject := "Mail delivery receipt"
	if rec.Result.Subject != "" {
		subject += ": " + rec.Result.Subject
	}
	fmt.Fprintf(w, "From: %s\r\n", sanitizeHeader(from))
	fmt.Fprintf(w, "To: %s\r\n", sanitizeHeader(to))
	fmt.Fprintf(w, "Date: %s\r\n", now.UTC().Format(time.RFC1123Z))
	fmt.Fprintf(w, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", sanitizeHeader(subject)))
	fmt.Fprint(w, "Auto-Submitted: auto-generated\r\n")
	fmt.Fprint(w, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(w, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(w, "--%s\r\n", boundary)
	fmt.Fprint(w, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprint(w, "Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	fmt.Fprint(w, textBody)
	fmt.Fprintf(w, "\r\n--%s\r\n", boundary)
	fmt.Fprint(w, "Content-Type: application/json; name=\"mailreceipt.json\"\r\n")
	fmt.Fprint(w, "Content-Disposition: attachment; filename=\"mailreceipt.json\"\r\n")
	fmt.Fprint(w, "Content-Transfer-Encoding: base64\r\n\r\n")
	writeBase64Body(w, jsonBody)
	fmt.Fprintf(w, "--%s--\r\n", boundary)
	return nil
}

// WO-26: encode human replies for conservative mail transports and clients.
func encodeQuotedPrintable(s string) (string, error) {
	var b strings.Builder
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// WO-22: production boundary source uses crypto randomness per generated reply.
func randomFilterReplyBoundary() (string, error) {
	raw := make([]byte, filterBoundaryRandomBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return filterReplyBoundaryPrefix + hex.EncodeToString(raw), nil
}

// WO-22: reject predictable or body-colliding MIME boundaries before writing.
func filterReplyBoundary(parts ...string) (string, error) {
	for attempt := 0; attempt < filterBoundaryMaxAttempts; attempt++ {
		boundary, err := filterNewBoundary()
		if err != nil {
			return "", err
		}
		if !isSafeFilterReplyBoundary(boundary) || filterBoundaryLinePresent(boundary, parts...) {
			continue
		}
		return boundary, nil
	}
	return "", fmt.Errorf("generate safe filter reply MIME boundary")
}

// WO-22: injected boundaries still must be safe for raw Content-Type use.
func isSafeFilterReplyBoundary(boundary string) bool {
	if boundary == filterLegacyReplyBoundary || len(boundary) > filterBoundaryMaxLength {
		return false
	}
	if !strings.HasPrefix(boundary, filterReplyBoundaryPrefix) {
		return false
	}
	for _, r := range boundary {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' {
			continue
		}
		return false
	}
	return true
}

// WO-22: a boundary candidate is unsafe if any part can already delimit it.
func filterBoundaryLinePresent(boundary string, parts ...string) bool {
	marker := "--" + boundary
	for _, part := range parts {
		for {
			line, rest, ok := strings.Cut(part, "\n")
			line = strings.TrimSuffix(line, "\r")
			if line == marker || line == marker+"--" {
				return true
			}
			if !ok {
				break
			}
			part = rest
		}
	}
	return false
}

// WO-19: base64-wrap JSON attachments for strict MIME transports.
func writeBase64Body(w io.Writer, raw []byte) {
	encoded := base64.StdEncoding.EncodeToString(raw)
	for len(encoded) > filterBase64LineLength {
		fmt.Fprintf(w, "%s\r\n", encoded[:filterBase64LineLength])
		encoded = encoded[filterBase64LineLength:]
	}
	if encoded != "" {
		fmt.Fprintf(w, "%s\r\n", encoded)
	}
}

func sanitizeHeader(s string) string {
	replacer := strings.NewReplacer("\r", " ", "\n", " ")
	return strings.TrimSpace(replacer.Replace(s))
}
