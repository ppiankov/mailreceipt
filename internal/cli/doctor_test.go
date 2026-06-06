package cli

import (
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

func hasPassing(r doctorReport, name string) bool {
	for _, c := range r.Checks {
		if c.Name == name && c.Status == "pass" {
			return true
		}
	}
	return false
}
