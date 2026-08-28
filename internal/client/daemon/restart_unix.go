//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

func restart(self string, args []string) error {
	return syscall.Exec(self, args, os.Environ())
}
