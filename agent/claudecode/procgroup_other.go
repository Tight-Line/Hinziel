//go:build !unix

package claudecode

import "os/exec"

func inOwnGroup(*exec.Cmd) {}
func killGroup(*exec.Cmd)  {}
