package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileIsNotError(t *testing.T) {
	_, ok, err := Load(filepath.Join(t.TempDir(), "absent.yml"))
	if err != nil {
		t.Fatalf("missing file must not error, got %v", err)
	}
	if ok {
		t.Fatal("missing file must report ok=false")
	}
}

func TestWriteThenLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	c, ok, err := Load(p)
	if err != nil || !ok {
		t.Fatalf("load after write: ok=%v err=%v", ok, err)
	}
	if c.Log != "/var/log/mail.log" {
		t.Fatalf("template log default, got %q", c.Log)
	}
	if c.LogYear != 2026 {
		t.Fatalf("template log_year default, got %d", c.LogYear)
	}
}

func TestWriteRefusesOverwriteWithoutForce(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	if err := Write(p, false); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, false); err == nil {
		t.Fatal("second write without force must refuse")
	}
	if err := Write(p, true); err != nil {
		t.Fatalf("force write must succeed, got %v", err)
	}
}

func TestLoadStripsQuotesAndIgnoresCommentsAndUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	body := "# a comment\nlog: ./mail.log\ncase_prefix: \"CASE-\"\nunknown_key: ignored\nlog_year: 2025\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Log != "./mail.log" {
		t.Fatalf("log, got %q", c.Log)
	}
	if c.CasePrefix != "CASE-" {
		t.Fatalf("quotes must be stripped, got %q", c.CasePrefix)
	}
	if c.LogYear != 2025 {
		t.Fatalf("log_year, got %d", c.LogYear)
	}
}

func TestLoadReceiptFilterConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	body := `log: ./mail.log
receipt_filter:
  domains: [acme.test]
  teams:
    docketing:
      members: [docketing@acme.test, Assistant1@acme.test, attorney1@acme.test]
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ReceiptFilter.Domains) != 1 || c.ReceiptFilter.Domains[0] != "acme.test" {
		t.Fatalf("receipt_filter domains: got %v", c.ReceiptFilter.Domains)
	}
	members := c.ReceiptFilter.Teams["docketing"]
	if len(members) != 3 {
		t.Fatalf("receipt_filter team members: got %v", members)
	}
	if members[1] != "assistant1@acme.test" {
		t.Fatalf("members should normalize case, got %v", members)
	}
}
