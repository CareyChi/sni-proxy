//go:build !linux

package privilege

import "errors"

func IsRoot() bool { return false }

func RequireRoot() error { return errors.New("privileged operations are supported only on Linux") }
