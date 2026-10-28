package install

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// SpawnDetached starts a process that survives the updater exiting. If logPath
// is non-empty, stdout and stderr are appended to it.
func SpawnDetached(executable string, args []string, dir, logPath string) (int, error) {
	cmd := exec.Command(executable, args...)
	cmd.Dir = dir
	var logFile *os.File
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		logFile = f
		cmd.Stdout, cmd.Stderr = f, f
	}
	cmd.Stdin = nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return 0, err
	}
	if logFile != nil {
		_ = logFile.Close()
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// WaitForProcesses waits until every PID no longer exists or ctx ends.
func WaitForProcesses(ctx context.Context, pids []int) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive := false
		for _, pid := range pids {
			if pid > 0 && processAlive(pid) {
				alive = true
				break
			}
		}
		if !alive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
