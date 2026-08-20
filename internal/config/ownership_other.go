//go:build !linux

package config

import "os"

func ownedByRoot(os.FileInfo) bool { return true }

func syncDirectory(string) error { return nil }
