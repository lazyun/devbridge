//go:build darwin

package appserver

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
)

func startIdentity(ctx context.Context, pid int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if info == nil || int(info.Proc.P_pid) != pid || info.Proc.P_starttime.Sec == 0 {
		return "", errors.New("appserver process not alive")
	}
	return fmt.Sprintf("darwin:%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
}
