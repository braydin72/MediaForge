//go:build !windows

package ffmpeg

import (
	"os"
	"syscall"
)

func readInode(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Ino
	}
	return 0
}
