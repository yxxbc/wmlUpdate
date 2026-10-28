package platform

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		os, arch string
		want     Platform
	}{
		{"macOS", "arm64", Platform{OS: "darwin", Arch: "arm64"}},
		{"windows", "x86_64", Platform{OS: "windows", Arch: "amd64"}},
		{"linux", "aarch64", Platform{OS: "linux", Arch: "arm64"}},
		{"win64", "x64", Platform{OS: "windows", Arch: "amd64"}},
		{"osx", "amd64", Platform{OS: "darwin", Arch: "amd64"}},
	}
	for _, tt := range tests {
		got, err := Normalize(tt.os, tt.arch)
		if err != nil {
			t.Fatalf("Normalize(%q, %q): %v", tt.os, tt.arch, err)
		}
		if got != tt.want {
			t.Errorf("Normalize(%q, %q) = %+v, want %+v", tt.os, tt.arch, got, tt.want)
		}
	}
	if _, err := Normalize("plan9", "amd64"); err == nil {
		t.Error("expected unsupported OS error")
	}
	if _, err := Normalize("linux", "riscv64"); err == nil {
		t.Error("expected unsupported architecture error")
	}
}

func TestAssetNameMatches(t *testing.T) {
	t.Parallel()
	darwinArm := Platform{OS: "darwin", Arch: "arm64"}
	tests := []struct {
		name string
		p    Platform
		want bool
	}{
		{"MyApp-1.2.0-macos-arm64.zip", darwinArm, true},
		{"MyApp-1.2.0-darwin-aarch64.tar.gz", darwinArm, true},
		{"MyApp-1.2.0-windows-amd64.zip", darwinArm, false},
		{"MyApp-1.2.0-linux-arm64.tar.gz", darwinArm, false},
		{"MyApp-1.2.0-macos-x86_64.zip", darwinArm, false},
		{"checksums.txt", darwinArm, false},
	}
	for _, tt := range tests {
		if got := AssetNameMatches(tt.name, tt.p); got != tt.want {
			t.Errorf("AssetNameMatches(%q, %v) = %v, want %v", tt.name, tt.p, got, tt.want)
		}
	}
}
