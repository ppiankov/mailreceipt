// Package cli wires the mailreceipt commands. It owns flag parsing and I/O;
// all logic lives in the analysis packages.
package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/ppiankov/mailreceipt/internal/config"
	"github.com/ppiankov/mailreceipt/internal/deliver"
	"github.com/ppiankov/mailreceipt/internal/eml"
	"github.com/ppiankov/mailreceipt/internal/maillog"
	"github.com/ppiankov/mailreceipt/internal/receipt"
	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags "-X ...cli.version=X.Y.Z".
var version = "dev"

// Root builds the command tree.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:           "mailreceipt",
		Short:         "Turn a dropped email + mail log into a cited delivery receipt",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(checkCmd(), verifyCmd(), initCmd(), doctorCmd())
	return root
}

func checkCmd() *cobra.Command {
	var (
		logPath  string
		caseRef  string
		format   string
		logYear  int
		emailArg string
	)
	cmd := &cobra.Command{
		Use:   "check [email.eml]",
		Short: "Check whether a dropped email was delivered, per the mail log",
		Long: "Reads a dropped email (RFC822 or a pasted top-of-thread block) and a\n" +
			"Postfix mail log, then reports a cited delivery outcome per recipient.\n\n" +
			"The email may be a file argument or piped on stdin.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				emailArg = args[0]
			}

			// .mailreceipt.yml supplies defaults; explicit flags override.
			if cfg, ok, _ := config.Load(config.FileName); ok {
				if logPath == "" && !cmd.Flags().Changed("log") {
					logPath = cfg.Log
				}
				if !cmd.Flags().Changed("log-year") && cfg.LogYear != 0 {
					logYear = cfg.LogYear
				}
				if cfg.CasePrefix != "" {
					caseRef = cfg.CasePrefix + caseRef
				}
			}

			emailReader, closeEmail, err := openInput(emailArg)
			if err != nil {
				return err
			}
			defer closeEmail()

			e, err := eml.Parse(emailReader)
			if err != nil {
				return fmt.Errorf("parsing email: %w", err)
			}
			if len(e.Recipients()) == 0 {
				return fmt.Errorf("no recipients found in the email (need To:/Cc:)")
			}

			if logPath == "" {
				return fmt.Errorf("--log <mail.log> is required")
			}
			lf, err := os.Open(logPath)
			if err != nil {
				return fmt.Errorf("opening log: %w", err)
			}
			defer lf.Close()
			log := maillog.Parse(lf, logYear)

			res := deliver.Analyze(e, log)
			rec := receipt.New(res, caseRef, time.Time{})

			return writeReceipt(cmd, rec, format)
		},
	}
	cmd.Flags().StringVar(&logPath, "log", "", "path to the Postfix mail log (required)")
	cmd.Flags().StringVar(&caseRef, "case", "", "case/matter reference to stamp on the receipt")
	cmd.Flags().StringVar(&format, "format", "md", "output format: md | json")
	cmd.Flags().IntVar(&logYear, "log-year", 0, "year for the year-less syslog timestamps (default 2026)")
	return cmd
}

// verifyCmd re-checks a JSON receipt against a fresh read of the log: every
// cited line must still be present verbatim. This is the auditor's command — it
// proves the receipt was not fabricated or edited.
func verifyCmd() *cobra.Command {
	var logPath string
	cmd := &cobra.Command{
		Use:   "verify <receipt.json>",
		Short: "Verify every citation in a receipt still appears verbatim in the log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if logPath == "" && !cmd.Flags().Changed("log") {
				if cfg, ok, _ := config.Load(config.FileName); ok {
					logPath = cfg.Log
				}
			}
			if logPath == "" {
				return fmt.Errorf("--log <mail.log> is required")
			}
			rec, err := receipt.LoadJSON(args[0])
			if err != nil {
				return fmt.Errorf("loading receipt: %w", err)
			}
			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				return fmt.Errorf("opening log: %w", err)
			}
			missing := rec.VerifyCitations(string(logBytes))
			if len(missing) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "OK: all %d citation(s) present in %s\n",
					rec.CitationCount(), logPath)
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "FAIL: %d citation(s) not found in %s:\n", len(missing), logPath)
			for _, m := range missing {
				fmt.Fprintf(cmd.ErrOrStderr(), "  - %s\n", m)
			}
			return fmt.Errorf("citation verification failed")
		},
	}
	cmd.Flags().StringVar(&logPath, "log", "", "path to the Postfix mail log to verify against (required)")
	return cmd
}
