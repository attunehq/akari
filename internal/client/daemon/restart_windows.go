//go:build windows

package daemon

func restart(self string, args []string) error {
	proc, err := spawnDetached(self, args[1:])
	if err != nil {
		return err
	}
	_ = proc.Release()
	return nil
}
