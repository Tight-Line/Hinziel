package hinziel

import "syscall"

// protectEnvironment makes this process non-dumpable, so other processes of
// the same user, the agent among them, can't read its environment (and the
// factory's credentials in it) from /proc/<pid>/environ. Children get the
// usual setting back when they exec.
func protectEnvironment() {
	const prSetDumpable = 4
	syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0)
}
