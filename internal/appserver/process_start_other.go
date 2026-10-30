//go:build !darwin

package appserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

func startIdentity(ctx context.Context, pid int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(raw), ")")
	if end < 0 {
		return "", errors.New("invalid process identity")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return "", errors.New("incomplete process identity")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return "linux:" + strings.TrimSpace(string(boot)) + ":" + fields[19], nil
}
