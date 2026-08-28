package daemon

import (
	"fmt"
	"strings"
)

// Restart replaces this process with self. args is the full argv, including
// args[0]. The caller must have released the daemon lock and closed the
// control socket first. On Unix this is exec and does not return on success.
// On Windows it detaches a new process and returns so the caller can exit.
func Restart(self string, args []string) error {
	if strings.TrimSpace(self) == "" {
		return fmt.Errorf("akari executable path is required")
	}
	if len(args) == 0 {
		return fmt.Errorf("restart arguments are required")
	}
	return restart(self, args)
}
