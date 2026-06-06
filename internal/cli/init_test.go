package cli

import (
	"os"
	"testing"

	"github.com/ppiankov/mailreceipt/internal/config"
)

// chdir into a temp dir for the duration of the test so init writes there.
func inTempDir(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

func TestInitWritesConfig(t *testing.T) {
	inTempDir(t)
	cmd := initCmd()
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init should succeed, got %v", err)
	}
	if _, err := os.Stat(config.FileName); err != nil {
		t.Fatalf("init must create %s, got %v", config.FileName, err)
	}
}

func TestInitRefusesOverwriteThenForce(t *testing.T) {
	inTempDir(t)
	if err := initCmd().Execute(); err != nil {
		t.Fatal(err)
	}
	if err := initCmd().Execute(); err == nil {
		t.Fatal("second init without --force must fail")
	}
	forced := initCmd()
	forced.SetArgs([]string{"--force"})
	if err := forced.Execute(); err != nil {
		t.Fatalf("init --force must succeed, got %v", err)
	}
}
