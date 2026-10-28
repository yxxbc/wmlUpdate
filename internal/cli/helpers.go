package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/yxxbc/wmlUpdate/internal/config"
	"github.com/yxxbc/wmlUpdate/internal/engine"
	"github.com/yxxbc/wmlUpdate/internal/platform"
)

func (a *app) loadConfig() (*config.Config, error) {
	path := a.configPath
	if path == "" {
		path = os.Getenv("WML_UPDATE_CONFIG")
	}
	if path == "" {
		for _, candidate := range []string{"updater.json", ".wml-update.json", "wml-update.json"} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return nil, usageError("no configuration file found; use --config or create updater.json")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, &engine.Error{Code: "config", Err: err}
	}
	if a.channel != "" {
		channel := strings.ToLower(strings.TrimSpace(a.channel))
		if channel == "" || strings.ContainsAny(channel, `/\`) {
			return nil, usageError("invalid --channel")
		}
		cfg.Channel = channel
	}
	if a.prerelease {
		cfg.Source.IncludePrerelease = true
	}
	return cfg, nil
}

func (a *app) targetPlatform() (platform.Platform, error) {
	p := platform.Current()
	var err error
	if a.osName != "" {
		p.OS, err = platform.NormalizeOS(a.osName)
		if err != nil {
			return p, usageError(err.Error())
		}
	}
	if a.archName != "" {
		p.Arch, err = platform.NormalizeArch(a.archName)
		if err != nil {
			return p, usageError(err.Error())
		}
	}
	return p, nil
}

func (a *app) newEngine(cfg *config.Config, p platform.Platform) (*engine.Engine, error) {
	eng, err := engine.New(cfg, p, a.sink())
	if err != nil {
		return nil, err
	}
	return eng, nil
}

func (a *app) prepared() (*config.Config, platform.Platform, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, platform.Platform{}, err
	}
	p, err := a.targetPlatform()
	if err != nil {
		return nil, platform.Platform{}, err
	}
	return cfg, p, nil
}

func requireNonEmpty(flag, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("--%s is required", flag)
	}
	return nil
}
