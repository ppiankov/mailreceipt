package cli

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/ppiankov/mailreceipt/internal/config"
	"github.com/ppiankov/mailreceipt/internal/deliver"
	"github.com/ppiankov/mailreceipt/internal/eml"
	"github.com/ppiankov/mailreceipt/internal/maillog"
	"github.com/ppiankov/mailreceipt/internal/receipt"
	"github.com/spf13/cobra"
)

const filterReplyBoundary = "mailreceipt-filter-reply"

func filterCmd() *cobra.Command {
	var (
		logPath      string
		logYear      int
		caseRef      string
		envelopeFrom string
		replyFrom    string
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

			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return nil
			}
			if filterLoopGuard(raw) {
				return nil
			}

			triggerSender := normalizeAddress(envelopeFrom)
			if !domainAllowed(triggerSender, cfg.ReceiptFilter.Domains) {
				return nil
			}
			replyFrom = normalizeAddress(replyFrom)
			if replyFrom == "" {
				// WO-16: keep generated replies RFC5322-complete even without explicit config.
				replyFrom = "mailreceipt@" + addressDomain(triggerSender)
			}
			if !domainAllowed(replyFrom, cfg.ReceiptFilter.Domains) {
				return nil
			}
			forwarded, err := eml.ExtractForwardedEmail(raw)
			if err != nil || len(forwarded.Email.Recipients()) == 0 {
				return nil
			}
			if !sharesFilterTeam(cfg.ReceiptFilter, triggerSender, forwarded.Email) {
				return nil
			}
			if logPath == "" {
				return nil
			}
			lf, err := os.Open(logPath)
			if err != nil {
				return nil
			}
			defer lf.Close()
			log := maillog.Parse(lf, logYear)

			e := forwarded.Email
			if forwarded.Attached {
				// WO-13: an attached .eml is a selector, not evidence; require
				// exact Message-ID correlation and never borrow recipient-window lines.
				e.Date = time.Time{}
			}
			res := deliver.Analyze(e, log)
			rec := receipt.New(res, caseRef, time.Time{})
			return writeFilterReply(cmd.OutOrStdout(), triggerSender, replyFrom, filterNow(), rec)
		},
	}
	cmd.Flags().StringVar(&envelopeFrom, "envelope-from", "", "MTA-authenticated envelope sender for the trigger email")
	cmd.Flags().StringVar(&replyFrom, "from", "", "From address for generated receipt replies")
	cmd.Flags().StringVar(&logPath, "log", "", "path to the Postfix mail log")
	cmd.Flags().StringVar(&caseRef, "case", "", "case/matter reference to stamp on the receipt")
	cmd.Flags().IntVar(&logYear, "log-year", 0, "year for the year-less syslog timestamps (default 2026)")
	return cmd
}

// WO-16: injectable clock keeps reply Date tests deterministic.
var filterNow = time.Now

func filterLoopGuard(raw []byte) bool {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return true
	}
	autoSubmitted := strings.TrimSpace(msg.Header.Get("Auto-Submitted"))
	if autoSubmitted != "" && !strings.EqualFold(autoSubmitted, "no") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(msg.Header.Get("Precedence")), "bulk")
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
			addr := normalizeAddress(m)
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
	if addr, err := mail.ParseAddress(v); err == nil {
		return strings.ToLower(addr.Address)
	}
	if addrs, err := mail.ParseAddressList(v); err == nil && len(addrs) > 0 {
		return strings.ToLower(addrs[0].Address)
	}
	for _, tok := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '<' || r == '>' || r == '\'' || r == '"'
	}) {
		if strings.Contains(tok, "@") {
			return strings.ToLower(strings.TrimSpace(tok))
		}
	}
	return strings.ToLower(v)
}

func writeFilterReply(w io.Writer, to, from string, now time.Time, rec receipt.Receipt) error {
	jsonBody, err := rec.JSON()
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
	fmt.Fprintf(w, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", filterReplyBoundary)
	fmt.Fprintf(w, "--%s\r\n", filterReplyBoundary)
	fmt.Fprint(w, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprint(w, "Content-Transfer-Encoding: 8bit\r\n\r\n")
	fmt.Fprint(w, rec.Markdown())
	fmt.Fprintf(w, "\r\n--%s\r\n", filterReplyBoundary)
	fmt.Fprint(w, "Content-Type: application/json; name=\"mailreceipt.json\"\r\n")
	fmt.Fprint(w, "Content-Disposition: attachment; filename=\"mailreceipt.json\"\r\n")
	fmt.Fprint(w, "Content-Transfer-Encoding: 7bit\r\n\r\n")
	fmt.Fprintln(w, string(jsonBody))
	fmt.Fprintf(w, "--%s--\r\n", filterReplyBoundary)
	return nil
}

func sanitizeHeader(s string) string {
	replacer := strings.NewReplacer("\r", " ", "\n", " ")
	return strings.TrimSpace(replacer.Replace(s))
}
