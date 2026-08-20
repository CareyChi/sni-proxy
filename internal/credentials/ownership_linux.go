//go:build linux

package credentials

import (
	"errors"
	"os"
	"syscall"
)

func matchOwnership(file *os.File, reference os.FileInfo) error {
	referenceStat, ok := reference.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine credential owner")
	}
	current, err := file.Stat()
	if err != nil {
		return err
	}
	currentStat, ok := current.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine temporary credential owner")
	}
	if currentStat.Uid == referenceStat.Uid && currentStat.Gid == referenceStat.Gid {
		return nil
	}
	return file.Chown(int(referenceStat.Uid), int(referenceStat.Gid))
}
