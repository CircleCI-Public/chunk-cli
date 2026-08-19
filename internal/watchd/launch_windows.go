//go:build windows

package watchd

import "os/exec"

func detachProcess(_ *exec.Cmd) {}
