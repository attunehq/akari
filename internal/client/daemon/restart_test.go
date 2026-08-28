package daemon

import (
	"path/filepath"
	"testing"
)

func TestRestartRejectsEmptyPath(t *testing.T) {
	if err := Restart("", []string{"akari", "daemon", "run"}); err == nil {
		t.Fatal("Restart accepted an empty executable path")
	}
	if err := Restart("   ", []string{"akari"}); err == nil {
		t.Fatal("Restart accepted a blank executable path")
	}
}

func TestRestartRejectsEmptyArgs(t *testing.T) {
	if err := Restart("/bin/akari", nil); err == nil {
		t.Fatal("Restart accepted empty argv")
	}
}

func TestRestartMissingBinary(t *testing.T) {
	self := filepath.Join(t.TempDir(), "missing-akari")
	err := Restart(self, []string{self, "daemon", "run"})
	if err == nil {
		t.Fatal("Restart accepted a missing binary")
	}
}
