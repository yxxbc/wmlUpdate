// Package platform normalizes operating system and architecture names and
// selects platform-specific assets.
package platform

import (
	"fmt"
	"runtime"
	"strings"
)

// Platform identifies a target system using Go's GOOS/GOARCH vocabulary.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Current returns the platform this process is running on.
func Current() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// String returns "os-arch".
func (p Platform) String() string { return p.OS + "-" + p.Arch }

// Normalize maps common human and vendor spellings to Go's naming.
func Normalize(osName, archName string) (Platform, error) {
	osName, err := NormalizeOS(osName)
	if err != nil {
		return Platform{}, err
	}
	archName, err = NormalizeArch(archName)
	if err != nil {
		return Platform{}, err
	}
	return Platform{OS: osName, Arch: archName}, nil
}

// NormalizeOS maps common operating system spellings to Go GOOS values.
func NormalizeOS(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "darwin", "macos", "mac", "osx", "macosx", "apple":
		return "darwin", nil
	case "windows", "win", "win32", "win64", "microsoft":
		return "windows", nil
	case "linux", "gnu/linux":
		return "linux", nil
	case "freebsd", "openbsd", "netbsd":
		return strings.ToLower(strings.TrimSpace(s)), nil
	default:
		return "", fmt.Errorf("unsupported operating system %q (use windows, darwin or linux)", s)
	}
}

// NormalizeArch maps common architecture spellings to Go GOARCH values.
func NormalizeArch(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "amd64", "x86_64", "x64", "x86-64", "64bit", "64-bit":
		return "amd64", nil
	case "arm64", "aarch64", "armv8", "arm64v8":
		return "arm64", nil
	case "386", "x86", "i386", "i686", "32bit", "32-bit":
		return "386", nil
	case "arm", "armv7", "armv7l", "armhf":
		return "arm", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q (use amd64, arm64, 386 or arm)", s)
	}
}

// AssetNameMatches applies a conservative filename heuristic. It is used only
// when no explicit asset rule matched. The filename must name our platform and
// must not name a different supported platform.
func AssetNameMatches(name string, p Platform) bool {
	n := strings.ToLower(name)
	tokens := nameTokens(n)
	has := func(values ...string) bool {
		for _, v := range values {
			if _, ok := tokens[v]; ok {
				return true
			}
		}
		return false
	}
	contains := func(values ...string) bool {
		for _, v := range values {
			if strings.Contains(n, v) {
				return true
			}
		}
		return false
	}

	osName := ""
	switch {
	case has("windows", "win32", "win64", "win") || contains("windows-", "windows_", "windows."):
		osName = "windows"
	case has("darwin", "macos", "macosx", "osx", "mac", "apple") || contains("macos-", "macos_", "macos."):
		osName = "darwin"
	case has("linux") || contains("linux-", "linux_", "linux."):
		osName = "linux"
	}
	if osName == "" || osName != p.OS {
		return false
	}

	detectedArch := ""
	switch {
	case contains("aarch64", "arm64"):
		detectedArch = "arm64"
	case contains("x86_64", "amd64") || has("x64"):
		detectedArch = "amd64"
	case contains("i386", "i686") || has("386", "x86"):
		detectedArch = "386"
	case contains("armv7", "armhf") || has("arm"):
		detectedArch = "arm"
	case contains("universal"):
		return true // macOS universal binaries support amd64 and arm64
	}
	return detectedArch == p.Arch
}

func nameTokens(n string) map[string]struct{} {
	out := make(map[string]struct{})
	start := -1
	for i, r := range n {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out[n[start:i]] = struct{}{}
			start = -1
		}
	}
	if start >= 0 {
		out[n[start:]] = struct{}{}
	}
	return out
}
