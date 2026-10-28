package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yxxbc/wmlUpdate/internal/buildinfo"
	"github.com/yxxbc/wmlUpdate/internal/engine"
	"github.com/yxxbc/wmlUpdate/internal/state"
)

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.jsonOut {
				return a.emitObject(map[string]string{
					"version": buildinfo.Version,
					"commit":  buildinfo.Commit,
					"built":   buildinfo.Date,
					"goos":    runtime.GOOS,
					"goarch":  runtime.GOARCH,
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Full())
			return nil
		},
	}
}

func (a *app) checkCmd() *cobra.Command {
	var current string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check for an update without downloading it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, p, err := a.prepared()
			if err != nil {
				return err
			}
			eng, err := a.newEngine(cfg, p)
			if err != nil {
				return err
			}
			defer eng.Close()
			res, err := eng.Check(cmd.Context(), current)
			if err != nil {
				return err
			}
			return a.emitResult(res)
		},
	}
	cmd.Flags().StringVar(&current, "current-version", "", "current application version (overrides config and state)")
	return cmd
}

func (a *app) updateCmd() *cobra.Command {
	var (
		current  string
		force    bool
		deferRun bool
		waitPIDs []int
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check, download, verify and install the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, p, err := a.prepared()
			if err != nil {
				return err
			}
			eng, err := a.newEngine(cfg, p)
			if err != nil {
				return err
			}
			defer eng.Close()
			if dryRun {
				res, err := eng.Check(cmd.Context(), current)
				if err != nil {
					return err
				}
				return a.emitResult(res)
			}
			if len(waitPIDs) > 0 && !deferRun {
				return usageError("--wait-pid requires --defer")
			}
			res, err := eng.Update(cmd.Context(), engine.UpdateOptions{
				CurrentVersion: current,
				Force:          force,
				Defer:          deferRun,
				WaitPIDs:       waitPIDs,
			})
			if err != nil {
				return err
			}
			return a.emitResult(res)
		},
	}
	cmd.Flags().StringVar(&current, "current-version", "", "current application version")
	cmd.Flags().BoolVar(&force, "force", false, "install the latest release even if not newer")
	cmd.Flags().BoolVar(&deferRun, "defer", false, "stage now and apply from a detached helper after the app exits")
	cmd.Flags().IntSliceVar(&waitPIDs, "wait-pid", nil, "PID to wait for before applying (repeatable; requires --defer)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only check; do not download or install")
	return cmd
}

func (a *app) fetchCmd() *cobra.Command {
	var current, output string
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Download and verify an artifact without installing it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, p, err := a.prepared()
			if err != nil {
				return err
			}
			eng, err := a.newEngine(cfg, p)
			if err != nil {
				return err
			}
			defer eng.Close()
			res, err := eng.Fetch(cmd.Context(), engine.FetchOptions{CurrentVersion: current, Output: output})
			if err != nil {
				return err
			}
			return a.emitResult(res)
		},
	}
	cmd.Flags().StringVar(&current, "current-version", "", "current application version")
	cmd.Flags().StringVarP(&output, "output", "o", "", "copy the verified artifact to this path")
	return cmd
}

func (a *app) rollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "Restore the most recent retained backup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, p, err := a.prepared()
			if err != nil {
				return err
			}
			eng, err := a.newEngine(cfg, p)
			if err != nil {
				return err
			}
			defer eng.Close()
			res, err := eng.Rollback(cmd.Context())
			if err != nil {
				return err
			}
			return a.emitResult(res)
		},
	}
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show persisted updater state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, p, err := a.prepared()
			if err != nil {
				return err
			}
			eng, err := a.newEngine(cfg, p)
			if err != nil {
				return err
			}
			defer eng.Close()
			st, err := eng.Status()
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.emitObject(st)
			}
			printStatus(cmd.OutOrStdout(), st, cfg.StateDir)
			return nil
		},
	}
}

func printStatus(w interface{ Write([]byte) (int, error) }, st *state.State, stateDir string) {
	fmt.Fprintf(w, "App:               %s\n", st.AppID)
	fmt.Fprintf(w, "Installed version: %s\n", fallback(st.InstalledVersion, "(unknown)"))
	fmt.Fprintf(w, "Channel:           %s\n", fallback(st.Channel, "(unknown)"))
	if !st.LastCheck.IsZero() {
		fmt.Fprintf(w, "Last check:        %s\n", st.LastCheck.Local().Format(time.RFC3339))
	}
	if st.LastUpdate != nil {
		fmt.Fprintf(w, "Last update:       %s at %s (%s)\n", st.LastUpdate.Version,
			st.LastUpdate.InstalledAt.Local().Format(time.RFC3339), st.LastUpdate.Strategy)
	}
	fmt.Fprintf(w, "Backups:           %d\n", len(st.Backups))
	for _, b := range st.Backups {
		fmt.Fprintf(w, "  - %s from %s at %s\n", b.Version, b.Target, b.CreatedAt.Local().Format(time.RFC3339))
	}
	if st.Pending != nil {
		fmt.Fprintf(w, "Pending install:   %s (%s, phase %s)\n", st.Pending.Version, st.Pending.Strategy, st.Pending.Phase)
	}
	fmt.Fprintf(w, "State directory:   %s\n", stateDir)
}

func (a *app) markCmd() *cobra.Command {
	var versionString, digest string
	cmd := &cobra.Command{
		Use:   "mark",
		Short: "Record a version installed by an external/custom installer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, p, err := a.prepared()
			if err != nil {
				return err
			}
			eng, err := a.newEngine(cfg, p)
			if err != nil {
				return err
			}
			defer eng.Close()
			res, err := eng.MarkInstalled(cmd.Context(), versionString, digest)
			if err != nil {
				return err
			}
			return a.emitResult(res)
		},
	}
	cmd.Flags().StringVar(&versionString, "version", "", "installed version")
	cmd.Flags().StringVar(&digest, "sha256", "", "SHA-256 of the installed artifact")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func (a *app) doctorCmd() *cobra.Command {
	var current string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose configuration, state, permissions and source connectivity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, p, err := a.prepared()
			if err != nil {
				return err
			}
			type check struct {
				Name    string `json:"name"`
				OK      bool   `json:"ok"`
				Detail  string `json:"detail,omitempty"`
				Warning bool   `json:"warning,omitempty"`
			}
			var checks []check
			add := func(name string, ok bool, detail string, warning bool) {
				checks = append(checks, check{Name: name, OK: ok, Detail: detail, Warning: warning})
			}
			add("config", true, cfg.File, false)
			add("state directory", true, cfg.StateDir, false)
			if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
				add("state directory writable", false, err.Error(), false)
			} else {
				f, err := os.CreateTemp(cfg.StateDir, ".doctor-*")
				if err != nil {
					add("state directory writable", false, err.Error(), false)
				} else {
					f.Close()
					os.Remove(f.Name())
					add("state directory writable", true, "", false)
				}
			}
			if cfg.Install.Type == "directory" || cfg.Install.Type == "file" {
				target, err := cfg.ResolveInstallPath(p)
				if err != nil {
					add("install path", false, err.Error(), false)
				} else {
					parent := filepath.Dir(target)
					info, err := os.Stat(parent)
					switch {
					case err != nil:
						add("install path", false, fmt.Sprintf("%s: %v", parent, err), false)
					case !info.IsDir():
						add("install path", false, fmt.Sprintf("%s is not a directory", parent), false)
					default:
						add("install path", true, target, false)
					}
					if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
						add("install target", false, "target is a symlink", false)
					}
				}
			}
			if cfg.Security.PublicKeyFile != "" || cfg.Security.PublicKey != "" {
				if _, err := cfg.PublicKey(); err != nil {
					add("signing key", false, err.Error(), false)
				} else {
					add("signing key", true, "Ed25519 public key loaded", false)
				}
			} else {
				add("signing key", true, "not configured (HTTPS trust only)", true)
			}
			eng, err := a.newEngine(cfg, p)
			if err != nil {
				add("engine", false, err.Error(), false)
			} else {
				defer eng.Close()
				res, err := eng.Check(cmd.Context(), current)
				if err != nil {
					add("source check", false, err.Error(), false)
				} else {
					add("source check", true, fmt.Sprintf("%s (current %s, latest %s)", res.Status, res.CurrentVersion, res.LatestVersion), false)
				}
				if st, err := eng.Status(); err == nil && st.Pending != nil {
					add("pending transaction", false, fmt.Sprintf("%s phase %s", st.Pending.Version, st.Pending.Phase), true)
				}
			}
			failed := false
			for _, c := range checks {
				if !c.OK && !c.Warning {
					failed = true
				}
			}
			if a.jsonOut {
				_ = a.emitObject(map[string]any{"ok": !failed, "checks": checks})
			} else {
				for _, c := range checks {
					mark := "ok"
					if !c.OK {
						mark = "FAIL"
						if c.Warning {
							mark = "warn"
						}
					} else if c.Warning {
						mark = "warn"
					}
					line := fmt.Sprintf("[%-4s] %s", mark, c.Name)
					if c.Detail != "" {
						line += ": " + c.Detail
					}
					fmt.Fprintln(cmd.OutOrStdout(), line)
				}
			}
			if failed {
				return exitOnly{Code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&current, "current-version", "", "current application version")
	return cmd
}

func (a *app) applyHelperCmd() *cobra.Command {
	var (
		planPath    string
		waitPIDs    []int
		waitTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:    "__apply",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if planPath == "" {
				return usageError("--plan is required")
			}
			return engine.RunHelper(cmd.Context(), planPath, waitPIDs, waitTimeout, a.sink())
		},
	}
	cmd.Flags().StringVar(&planPath, "plan", "", "plan file")
	cmd.Flags().IntSliceVar(&waitPIDs, "wait-pid", nil, "PID to wait for")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "maximum time to wait for the application to exit")
	return cmd
}

type exitOnly struct{ Code int }

func (e exitOnly) Error() string { return "diagnostics reported failures" }

func fallback(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
