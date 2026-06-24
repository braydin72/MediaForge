//go:build windows

package browse

import "os"

func readInode(info os.FileInfo) uint64 {
	return 0
}
