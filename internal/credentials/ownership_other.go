//go:build !linux

package credentials

import "os"

func matchOwnership(*os.File, os.FileInfo) error { return nil }
