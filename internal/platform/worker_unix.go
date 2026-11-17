//go:build !windows

package platform

import "os/exec"

func StartWorker(cmd *exec.Cmd) (func(), func() error, error) {
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return func() {}, func() error { return nil }, nil
}
