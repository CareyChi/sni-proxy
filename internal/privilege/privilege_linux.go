//go:build linux

package privilege

import (
	"errors"
	"os"
)

func IsRoot() bool { return os.Geteuid() == 0 }

func RequireRoot() error {
	if !IsRoot() {
		return errors.New("operation requires root privileges")
	}
	return nil
}
