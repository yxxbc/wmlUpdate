package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/yxxbc/wmlUpdate/internal/event"
	"github.com/yxxbc/wmlUpdate/internal/install"
	"github.com/yxxbc/wmlUpdate/internal/lock"
	"github.com/yxxbc/wmlUpdate/internal/state"
)

type helperResult struct {
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

func (e *Engine) schedule(ctx context.Context, plan *install.Plan, waitPIDs []int) (int, error) {
	planPath := install.PlanPath(e.cfg.StateDir, plan.ID)
	if err := install.WritePlan(planPath, plan); err != nil {
		return 0, fmt.Errorf("write helper plan: %w", err)
	}
	helperDir := filepath.Join(e.cfg.StateDir, "helper", plan.ID)
	if err := os.MkdirAll(helperDir, 0o700); err != nil {
		return 0, fmt.Errorf("create helper directory: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate updater executable: %w", err)
	}
	name := "wml-update"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	helperPath := filepath.Join(helperDir, name)
	if err := copyExecutable(exe, helperPath); err != nil {
		return 0, fmt.Errorf("copy helper executable: %w", err)
	}
	args := []string{"__apply", "--plan", planPath}
	for _, pid := range waitPIDs {
		if pid > 0 {
			args = append(args, "--wait-pid", strconv.Itoa(pid))
		}
	}
	logPath := filepath.Join(helperDir, "helper.log")
	pid, err := install.SpawnDetached(helperPath, args, helperDir, logPath)
	if err != nil {
		return 0, fmt.Errorf("start helper: %w", err)
	}
	e.cleanupOldHelpers(plan.ID)
	e.sink.Emit(event.Event{
		Type:    event.TypeLog,
		Phase:   "install",
		Message: fmt.Sprintf("deferred install scheduled (helper pid %d)", pid),
	})
	_ = ctx
	return pid, nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, 0o700)
}

func (e *Engine) cleanupOldHelpers(currentID string) {
	root := filepath.Join(e.cfg.StateDir, "helper")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == currentID {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, entry.Name()))
	}
}

// RunHelper executes a deferred plan. It is invoked by the hidden __apply
// command in a copied updater process.
func RunHelper(ctx context.Context, planPath string, waitPIDs []int, waitTimeout time.Duration, sink event.Sink) error {
	plan, err := install.ReadPlan(planPath)
	if err != nil {
		return fmt.Errorf("read helper plan: %w", err)
	}
	if sink == nil {
		sink = event.NopSink{}
	}
	result := &helperResult{}
	fail := func(err error) error {
		result.Error = err.Error()
		writeHelperResult(planPath, result)
		cleanupPlan(plan)
		return err
	}
	if len(waitPIDs) > 0 {
		if waitTimeout <= 0 {
			waitTimeout = 5 * time.Minute
		}
		waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		err := install.WaitForProcesses(waitCtx, waitPIDs)
		cancel()
		if err != nil {
			return fail(fmt.Errorf("wait for application exit: %w", err))
		}
	}
	store, err := state.Open(plan.StateDir)
	if err != nil {
		return fail(err)
	}
	lk, err := lock.Acquire(ctx, plan.StateDir, 5*time.Minute)
	if err != nil {
		return fail(fmt.Errorf("acquire update lock: %w", err))
	}
	defer lk.Release()
	st, err := store.Load(plan.AppID)
	if err != nil {
		return fail(err)
	}
	mgr := &install.Manager{Store: store, Data: st, KeepBackups: plan.KeepBackups, Sink: sink}
	warnings, err := mgr.Recover()
	for _, w := range warnings {
		sink.Emit(event.Event{Type: event.TypeWarning, Phase: "recover", Message: w})
	}
	if err != nil {
		return fail(err)
	}
	if err := mgr.Apply(ctx, plan); err != nil {
		return fail(err)
	}
	result.OK = true
	writeHelperResult(planPath, result)
	return nil
}

func writeHelperResult(planPath string, result *helperResult) {
	result.FinishedAt = time.Now().UTC()
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(planPath+".result", append(data, '\n'), 0o600)
}

// HelperResultPath is exposed for integrations that want to inspect deferred
// install outcomes.
func HelperResultPath(stateDir, planID string) string {
	return install.PlanPath(stateDir, planID) + ".result"
}
