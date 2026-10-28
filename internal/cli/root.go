package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/yxxbc/wmlUpdate/internal/engine"
	"github.com/yxxbc/wmlUpdate/internal/event"
)

type app struct {
	root *cobra.Command

	configPath string
	jsonOut    bool
	osName     string
	archName   string
	channel    string
	prerelease bool

	jsonSink *event.JSONWriter
}

func newApp() *app {
	a := &app{}
	root := &cobra.Command{
		Use:           "wml-update",
		Short:         "Cross-platform updater for GitHub Releases and self-hosted manifests",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.PersistentFlags().StringVarP(&a.configPath, "config", "c", "", "path to updater.json (default: WML_UPDATE_CONFIG, updater.json, .wml-update.json)")
	root.PersistentFlags().BoolVar(&a.jsonOut, "json", false, "emit one JSON object per line on stdout")
	root.PersistentFlags().StringVar(&a.osName, "os", "", "override target OS (windows, darwin, linux)")
	root.PersistentFlags().StringVar(&a.archName, "arch", "", "override target architecture (amd64, arm64, ...)")
	root.PersistentFlags().StringVar(&a.channel, "channel", "", "override update channel")
	root.PersistentFlags().BoolVar(&a.prerelease, "include-prerelease", false, "allow prerelease versions")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError(err.Error())
	})

	root.AddCommand(
		a.versionCmd(),
		a.checkCmd(),
		a.updateCmd(),
		a.fetchCmd(),
		a.rollbackCmd(),
		a.statusCmd(),
		a.markCmd(),
		a.doctorCmd(),
		a.serveCmd(),
		a.publishCmd(),
		a.keygenCmd(),
		a.signCmd(),
		a.verifyCmd(),
		a.applyHelperCmd(),
	)
	a.root = root
	return a
}

// Execute runs the CLI and returns a process exit code.
func Execute() int {
	a := newApp()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.root.SetContext(ctx)
	err := a.root.Execute()
	if err == nil {
		return 0
	}
	var only exitOnly
	if errors.As(err, &only) {
		return only.Code
	}
	a.emitError(err)
	return exitCode(err)
}

type usageError string

func (e usageError) Error() string { return string(e) }

func exitCode(err error) int {
	var ue usageError
	if errors.As(err, &ue) {
		return 2
	}
	var only exitOnly
	if errors.As(err, &only) {
		return only.Code
	}
	switch engine.CodeOf(err) {
	case "config":
		return 2
	case "source", "network", "no_release", "no_asset":
		return 3
	case "integrity":
		return 4
	case "install", "rollback":
		return 5
	case "locked", "state", "io":
		return 6
	case "cancelled":
		return 130
	default:
		return 1
	}
}
func (a *app) sink() event.Sink {
	if a.jsonOut {
		if a.jsonSink == nil {
			a.jsonSink = event.NewJSONWriter(os.Stdout)
		}
		return a.jsonSink
	}
	return &humanSink{w: os.Stderr}
}

func (a *app) emitError(err error) {
	code := engine.CodeOf(err)
	if a.jsonOut {
		e := event.Event{
			Type:  event.TypeError,
			Error: &event.Error{Code: code, Message: err.Error()},
		}
		a.sink().Emit(e)
		return
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
}

func (a *app) emitResult(res *engine.Result) error {
	if a.jsonOut {
		a.sink().Emit(event.Event{Type: event.TypeResult, Data: res})
		return nil
	}
	printResult(os.Stdout, res)
	return nil
}

func (a *app) emitObject(v any) error {
	if a.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(v)
	}
	return nil
}

type humanSink struct {
	w           io.Writer
	lastBucket  int
	lastPercent int
}

func (h *humanSink) Emit(e event.Event) {
	switch e.Type {
	case event.TypeLog:
		if e.Message != "" {
			fmt.Fprintf(h.w, "  %s\n", e.Message)
		}
	case event.TypeWarning:
		fmt.Fprintf(h.w, "warning: %s\n", e.Message)
	case event.TypeProgress:
		if e.Phase != "download" {
			return
		}
		pct := int(e.Percent)
		bucket := pct / 10
		if bucket == h.lastBucket && pct != 100 && e.Total > 0 {
			return
		}
		if pct == 100 && h.lastPercent == 100 {
			return
		}
		h.lastBucket = bucket
		h.lastPercent = pct
		if e.Total > 0 {
			fmt.Fprintf(h.w, "download: %3d%% (%s / %s)\n", pct, humanBytes(e.Bytes), humanBytes(e.Total))
		} else {
			fmt.Fprintf(h.w, "download: %s\n", humanBytes(e.Bytes))
		}
	}
}

func printResult(w io.Writer, r *engine.Result) {
	if r == nil {
		return
	}
	switch r.Status {
	case engine.StatusUpToDate:
		fmt.Fprintf(w, "Up to date: %s (channel %s)\n", r.CurrentVersion, r.Channel)
	case engine.StatusUpdateAvailable:
		fmt.Fprintf(w, "Update available: %s -> %s (channel %s)\n", r.CurrentVersion, r.LatestVersion, r.Channel)
		if r.Asset != nil {
			fmt.Fprintf(w, "Asset: %s (%s, %s)\n", r.Asset.Name, r.Asset.OS, r.Asset.Arch)
			if r.Asset.SHA256 != "" {
				fmt.Fprintf(w, "SHA-256: %s\n", r.Asset.SHA256)
			}
		}
		if len(r.Notes) > 0 {
			fmt.Fprintf(w, "\n%s\n", indent(strings.TrimSpace(limitText(r.Notes, 4000)), "  "))
		}
	case engine.StatusUpdated:
		fmt.Fprintf(w, "Updated: %s -> %s\n", r.PreviousVersion, r.CurrentVersion)
		if r.ArtifactPath != "" {
			fmt.Fprintf(w, "Artifact: %s\n", r.ArtifactPath)
		}
	case engine.StatusDownloaded:
		fmt.Fprintf(w, "Downloaded %s to %s\n", r.LatestVersion, r.ArtifactPath)
	case engine.StatusApplyScheduled:
		fmt.Fprintf(w, "Update scheduled: %s (plan %s, helper pid %d)\n", r.LatestVersion, r.PlanID, r.HelperPID)
	case engine.StatusRolledBack:
		fmt.Fprintf(w, "Rolled back: %s -> %s\n", r.PreviousVersion, r.CurrentVersion)
	case "marked":
		fmt.Fprintf(w, "Recorded installed version %s\n", r.CurrentVersion)
	default:
		fmt.Fprintf(w, "%s\n", r.Status)
	}
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func limitText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
