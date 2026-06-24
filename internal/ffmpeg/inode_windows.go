//go:build windows

package ffmpeg

import "os"

func readInode(info os.FileInfo) uint64 {
	return 0
}
