package sandbox

import (
	"os"
	"syscall"
)

func fileUID(st os.FileInfo) int { return int(st.Sys().(*syscall.Stat_t).Uid) }
