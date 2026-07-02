package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ppiankov/mailreceipt/internal/maillog"
	"github.com/spf13/cobra"
)

// doctorCheck is one diagnostic probe of the supplied log.
type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail
	Detail string `json:"detail"`
}

// doctorReport is the machine-readable diagnosis. The shape (top-level status +
// version, checks[].name/.status) matches the ANCC doctor contract so an agent can
// read it the same way it reads any ANCC tool's doctor output.
type doctorReport struct {
	Tool    string            `json:"tool"`
	Version string            `json:"version"`
	Source  map[string]string `json:"source"` // provenance, e.g. {"repo": "..."}
	Status  string            `json:"status"` // worst of the checks
	Checks  []doctorCheck     `json:"checks"`
}

// repoURL identifies where this binary came from, for doctor-output provenance.
const repoURL = "https://github.com/ppiankov/mailreceipt"

// doctorCmd diagnoses the mail log so "I could not read the evidence" never looks
// like "the evidence says not delivered". It answers the questions a not_found
// result cannot: does the log exist, is it readable, is it the right format.
func doctorCmd() *cobra.Command {
	var (
		logPath string
		format  string
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose a mail log before trusting a not_found result",
		Long: "Checks that the --log file exists, is readable and non-empty, what\n" +
			"timestamp format it uses, and how many Postfix delivery lines it holds.\n" +
			"A not_found receipt is only trustworthy once doctor reports the log is\n" +
			"readable and contains delivery lines.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if logPath == "" {
				return fmt.Errorf("--log <mail.log> is required")
			}
			format = strings.ToLower(strings.TrimSpace(format))
			switch format {
			case "", "md", "markdown", "json":
			default:
				// WO-10: reject unknown formats instead of silently emitting text.
				// Accepted set matches check/writeReceipt: md | markdown | json.
				return fmt.Errorf("unknown --format %q (use md, markdown, or json)", format)
			}
			rep := diagnose(logPath)
			switch format {
			case "json":
				b, err := json.MarshalIndent(rep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(b))
			case "", "md", "markdown":
				fmt.Fprint(cmd.OutOrStdout(), rep.text())
			}
			if rep.Status == "fail" {
				return errDiagnosisFailed
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&logPath, "log", "", "path, comma-list, or glob of Postfix mail logs to diagnose (required)")
	cmd.Flags().StringVar(&format, "format", "md", "output format: md | markdown | json")
	return cmd
}

// errDiagnosisFailed drives a non-zero exit when a check fails; the report on
// stdout carries the detail, this is just the exit-code signal.
var errDiagnosisFailed = fmt.Errorf("diagnosis found a failing check")

// diagnose runs the probes in order. Each later probe is skipped (recorded as the
// fail that blocks it) when an earlier prerequisite fails, so the report never
// claims "0 delivery lines" when the real problem is the file is unreadable.
func diagnose(logPath string) doctorReport {
	rep := doctorReport{
		Tool:    "mailreceipt",
		Version: version,
		Source:  map[string]string{"repo": repoURL},
	}

	logInput, err := readLogInput(logPath)
	if err != nil {
		rep.add("log_exists", "fail", fmt.Sprintf("%s: %v", logPath, err))
		rep.finish()
		return rep
	}
	rep.add("log_exists", "pass", strings.Join(logInput.Paths, ", "))
	rep.add("log_readable", "pass", "readable")

	if len(logInput.Data) == 0 {
		rep.add("log_nonempty", "fail", "the log input is empty")
		rep.finish()
		return rep
	}
	rep.add("log_nonempty", "pass", fmt.Sprintf("%d decoded bytes", len(logInput.Data)))

	log := maillog.Parse(bytes.NewReader(logInput.Data), 0)
	if n := len(log.Events); n == 0 {
		rep.add("delivery_lines", "warn",
			"no Postfix delivery lines parsed — wrong file, or not Postfix syslog?")
	} else {
		rep.add("delivery_lines", "pass", fmt.Sprintf("%d delivery event(s)", n))
	}
	if tsRaw, ok := log.CoverageTimeRaw(); ok {
		rep.add("timestamp_format", "pass", timestampFormat(tsRaw))
	}
	if _, _, ok := log.CoverageRange(); ok {
		rep.add("log_time_range", "pass", strings.TrimSuffix(logRangeSentence(log), "."))
	}

	rep.finish()
	return rep
}

// timestampFormat names the timestamp style of a raw log timestamp, so an operator
// knows whether --log-year applies (BSD) or is ignored (RFC3339 self-dates).
func timestampFormat(tsRaw string) string {
	if len(tsRaw) >= 5 && tsRaw[4] == '-' && strings.Contains(tsRaw, "T") {
		return "RFC3339 (self-dating; --log-year ignored)"
	}
	return "BSD syslog (year-less; set --log-year for old logs)"
}

func (r *doctorReport) add(name, status, detail string) {
	r.Checks = append(r.Checks, doctorCheck{Name: name, Status: status, Detail: detail})
}

// finish sets the overall status to the worst check (fail > warn > pass).
func (r *doctorReport) finish() {
	r.Status = "ok"
	worst := 0
	rank := map[string]int{"pass": 0, "warn": 1, "fail": 2}
	for _, c := range r.Checks {
		if rank[c.Status] > worst {
			worst = rank[c.Status]
		}
	}
	switch worst {
	case 1:
		r.Status = "warn"
	case 2:
		r.Status = "fail"
	}
}

func (r doctorReport) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mailreceipt doctor — overall: %s\n", r.Status)
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "  [%s] %s: %s\n", c.Status, c.Name, c.Detail)
	}
	return b.String()
}
