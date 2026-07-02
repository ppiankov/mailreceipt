package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bsdLog and rfcLog are minimal one-delivery logs in each timestamp format.
const bsdLog = `Jun  5 15:41:55 mail01 postfix/smtp[20460]: 7C2D9E1F02: to=<a@x.test>, relay=mx[1.2.3.4]:25, status=sent (250 OK)
`
const rfcLog = `2026-06-05T15:41:55.1+02:00 mail postfix/smtp[20460]: 7C2D9E1F02: to=<a@x.test>, relay=mx[1.2.3.4]:25, status=sent (250 OK)
`

func TestDiagnoseGoodLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mail.log")
	if err := os.WriteFile(p, []byte(bsdLog), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := diagnose(p)
	if rep.Status != "ok" {
		t.Fatalf("good log should be ok, got %s (%+v)", rep.Status, rep.Checks)
	}
	if !hasPassing(rep, "delivery_lines") {
		t.Fatal("delivery_lines should pass on a log with one delivery")
	}
}

func TestDiagnoseMissingLogFails(t *testing.T) {
	rep := diagnose(filepath.Join(t.TempDir(), "nope.log"))
	if rep.Status != "fail" {
		t.Fatalf("missing log must be fail, got %s", rep.Status)
	}
	if rep.Checks[0].Name != "log_exists" || rep.Checks[0].Status != "fail" {
		t.Fatalf("first check should be a failing log_exists, got %+v", rep.Checks[0])
	}
}

func TestDiagnoseEmptyLogFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rep := diagnose(p)
	if rep.Status != "fail" {
		t.Fatalf("empty log must fail, got %s", rep.Status)
	}
}

func TestDiagnoseWrongFormatWarns(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notmail.log")
	if err := os.WriteFile(p, []byte("this is not a postfix log\nhello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := diagnose(p)
	if rep.Status != "warn" {
		t.Fatalf("a non-postfix file should warn (no delivery lines), got %s", rep.Status)
	}
}

func TestDiagnoseDetectsRFC3339(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rfc.log")
	if err := os.WriteFile(p, []byte(rfcLog), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := diagnose(p)
	var tsDetail string
	for _, c := range rep.Checks {
		if c.Name == "timestamp_format" {
			tsDetail = c.Detail
		}
	}
	if !strings.Contains(tsDetail, "RFC3339") {
		t.Fatalf("should detect RFC3339, got %q", tsDetail)
	}
}

// WO-38: doctor reports the searched log timestamp coverage.
func TestDiagnoseReportsLogTimeRange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rfc.log")
	if err := os.WriteFile(p, []byte(rfcLog), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := diagnose(p)
	var rangeDetail string
	for _, c := range rep.Checks {
		if c.Name == "log_time_range" {
			rangeDetail = c.Detail
		}
	}
	if !strings.Contains(rangeDetail, "Searched log time range: 2026-06-05") {
		t.Fatalf("doctor should report searched range, got %q", rangeDetail)
	}
}

func TestDiagnoseReportsCoverageWithoutDeliveryLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "scanner.log")
	body := `2026-06-05T10:00:00+02:00 mail KLMS: clean: message-id="<scanner-only@example.test>": action="Skipped"
2026-06-05T10:30:00+02:00 mail postfix/qmgr[101]: idle
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := diagnose(p)
	if rep.Status != "warn" {
		t.Fatalf("timestamped log without delivery events should warn, got %s (%+v)", rep.Status, rep.Checks)
	}
	if !hasPassing(rep, "log_time_range") {
		t.Fatalf("log_time_range should pass from non-delivery timestamps, got %+v", rep.Checks)
	}
	var rangeDetail string
	for _, c := range rep.Checks {
		if c.Name == "log_time_range" {
			rangeDetail = c.Detail
		}
	}
	if !strings.Contains(rangeDetail, "2026-06-05 10:00:00 +0200 to 2026-06-05 10:30:00 +0200") {
		t.Fatalf("doctor should report full coverage from non-delivery lines, got %q", rangeDetail)
	}
}

// WO-38: doctor uses the same gzip/glob log input path as filter.
func TestDiagnoseReadsGzipGlob(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mail.log"), []byte("not a delivery\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeGzipFile(t, filepath.Join(dir, "mail.log.1.gz"), bsdLog)
	rep := diagnose(filepath.Join(dir, "mail.log*"))
	if rep.Status != "ok" {
		t.Fatalf("gzip glob log should be ok, got %s (%+v)", rep.Status, rep.Checks)
	}
	if !hasPassing(rep, "log_time_range") {
		t.Fatalf("log_time_range should pass on gzip glob, got %+v", rep.Checks)
	}
}

func TestDoctorReportIsValidANCCJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mail.log")
	if err := os.WriteFile(p, []byte(bsdLog), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(diagnose(p))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	// ANCC doctor contract: top-level status + version, checks[] with name+status.
	for _, k := range []string{"status", "version", "checks"} {
		if _, ok := doc[k]; !ok {
			t.Fatalf("doctor JSON missing required %q field", k)
		}
	}
	checks := doc["checks"].([]any)
	first := checks[0].(map[string]any)
	if _, ok := first["name"]; !ok {
		t.Fatal("checks[0] missing name")
	}
	if _, ok := first["status"]; !ok {
		t.Fatal("checks[0] missing status")
	}
}

// WO-10: the Cobra command must accept documented formats, not just the helper path.
func TestDoctorCommandAcceptsDocumentedFormats(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mail.log")
	if err := os.WriteFile(p, []byte(bsdLog), 0o644); err != nil {
		t.Fatal(err)
	}
	mdOut, _, err := runDoctorCommand(t, "--log", p, "--format", "md")
	if err != nil {
		t.Fatalf("doctor --format md should succeed: %v", err)
	}
	if !strings.Contains(mdOut, "mailreceipt doctor") {
		t.Fatalf("doctor --format md should emit text output, got %q", mdOut)
	}
	// WO-10: the `markdown` alias check accepts must match, the same text output.
	markdownOut, _, err := runDoctorCommand(t, "--log", p, "--format", "markdown")
	if err != nil {
		t.Fatalf("doctor --format markdown should succeed (alias of md): %v", err)
	}
	if markdownOut != mdOut {
		t.Fatalf("doctor --format markdown should match --format md output\nmd:       %q\nmarkdown: %q", mdOut, markdownOut)
	}
	defaultOut, _, err := runDoctorCommand(t, "--log", p)
	if err != nil {
		t.Fatalf("doctor default format should succeed: %v", err)
	}
	if !strings.Contains(defaultOut, "mailreceipt doctor") {
		t.Fatalf("doctor default format should emit text output, got %q", defaultOut)
	}
	jsonOut, _, err := runDoctorCommand(t, "--log", p, "--format", "json")
	if err != nil {
		t.Fatalf("doctor --format json should succeed: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatalf("doctor --format json should emit JSON: %v\n%s", err, jsonOut)
	}
}

// WO-10: unknown formats must not silently run as Markdown.
func TestDoctorCommandRejectsUnknownFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mail.log")
	if err := os.WriteFile(p, []byte(bsdLog), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runDoctorCommand(t, "--log", p, "--format", "typo")
	if err == nil {
		t.Fatalf("doctor --format typo should fail, got output:\n%s", out)
	}
	// WO-10 rev6: the error must name ALL accepted values, including markdown,
	// so the alias cannot be silently dropped from the wording again.
	msg := err.Error()
	if !strings.Contains(msg, `unknown --format "typo"`) {
		t.Fatalf("error should name the bad value, got %q", msg)
	}
	for _, v := range []string{"md", "markdown", "json"} {
		if !strings.Contains(msg, v) {
			t.Fatalf("error should advertise accepted value %q, got %q", v, msg)
		}
	}
	if out != "" {
		t.Fatalf("unknown format should not emit a fallback report, got:\n%s", out)
	}
}

// WO-10 rev6: doctor --help / flag usage must advertise all accepted formats,
// including the markdown alias, so the documented surface matches what is accepted.
func TestDoctorHelpAdvertisesAcceptedFormats(t *testing.T) {
	out, _, err := runDoctorCommand(t, "--help")
	if err != nil {
		t.Fatalf("doctor --help should succeed: %v", err)
	}
	for _, v := range []string{"md", "markdown", "json"} {
		if !strings.Contains(out, v) {
			t.Fatalf("doctor --help should advertise format %q, got:\n%s", v, out)
		}
	}
}

// WO-10: run the public Cobra path so flag handling is covered.
func runDoctorCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := Root()
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"doctor"}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func hasPassing(r doctorReport, name string) bool {
	for _, c := range r.Checks {
		if c.Name == name && c.Status == "pass" {
			return true
		}
	}
	return false
}
