//go:build !windows

package service

import "os/exec"

func hideCommandWindow(*exec.Cmd) {}
