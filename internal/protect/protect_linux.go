// Package protect keeps the factory's credentials away from the agent.
package protect

import "syscall"

// Environment makes this process non-dumpable, so other processes of the same
// user, the agent among them, can't read its environment (and the factory's
// credentials in it) from /proc/<pid>/environ. Children get the usual setting
// back when they exec.
func Environment() {
	const prSetDumpable = 4
	syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0)
}
