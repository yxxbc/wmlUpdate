// Package version implements strict Semantic Versioning 2.0.0 parsing and
// ordering. It intentionally does not coerce loose version strings: accepting
// ambiguous versions in an updater can silently install the wrong release.
package version

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version.
type Version struct {
	Major uint64
	Minor uint64
	Patch uint64
	Pre   []string
	Build string
}

// ErrInvalid is wrapped by every parse error.
var ErrInvalid = errors.New("invalid semantic version")

// Parse parses a strict semantic version. A single leading "v" or "V" is
// accepted because Git tags commonly use it.
func Parse(s string) (Version, error) {
	original := s
	s = strings.TrimSpace(s)
	if s == "" {
		return Version{}, fmt.Errorf("%w: empty string", ErrInvalid)
	}
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	if s == "" {
		return Version{}, fmt.Errorf("%w: %q", ErrInvalid, original)
	}

	var build string
	if i := strings.IndexByte(s, '+'); i >= 0 {
		build = s[i+1:]
		s = s[:i]
		if err := validateBuild(build); err != nil {
			return Version{}, fmt.Errorf("%w: %q: %v", ErrInvalid, original, err)
		}
	}

	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		prePart := s[i+1:]
		s = s[:i]
		if prePart == "" {
			return Version{}, fmt.Errorf("%w: %q: empty prerelease", ErrInvalid, original)
		}
		pre = strings.Split(prePart, ".")
		for _, p := range pre {
			if err := validatePreIdentifier(p); err != nil {
				return Version{}, fmt.Errorf("%w: %q: %v", ErrInvalid, original, err)
			}
		}
	}

	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%w: %q: expected MAJOR.MINOR.PATCH", ErrInvalid, original)
	}
	nums := make([]uint64, 3)
	for i, p := range parts {
		if p == "" {
			return Version{}, fmt.Errorf("%w: %q: empty numeric identifier", ErrInvalid, original)
		}
		if len(p) > 1 && p[0] == '0' {
			return Version{}, fmt.Errorf("%w: %q: leading zero in %q", ErrInvalid, original, p)
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return Version{}, fmt.Errorf("%w: %q: non-numeric identifier %q", ErrInvalid, original, p)
			}
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("%w: %q: %v", ErrInvalid, original, err)
		}
		nums[i] = n
	}

	return Version{
		Major: nums[0],
		Minor: nums[1],
		Patch: nums[2],
		Pre:   pre,
		Build: build,
	}, nil
}

// MustParse is Parse for callers that have already validated input.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

func validatePreIdentifier(s string) error {
	if s == "" {
		return errors.New("empty prerelease identifier")
	}
	numeric := true
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-':
			numeric = false
		default:
			return fmt.Errorf("invalid prerelease identifier %q", s)
		}
	}
	if numeric && len(s) > 1 && s[0] == '0' {
		return fmt.Errorf("leading zero in prerelease identifier %q", s)
	}
	return nil
}

func validateBuild(s string) error {
	if s == "" {
		return errors.New("empty build metadata")
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return errors.New("empty build metadata identifier")
		}
		for _, r := range part {
			if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' {
				continue
			}
			return fmt.Errorf("invalid build identifier %q", part)
		}
	}
	return nil
}

// Compare returns -1, 0, or 1 when v is lower than, equal to, or greater than
// other. Build metadata is ignored as required by SemVer.
func (v Version) Compare(other Version) int {
	if c := compareUint(v.Major, other.Major); c != 0 {
		return c
	}
	if c := compareUint(v.Minor, other.Minor); c != 0 {
		return c
	}
	if c := compareUint(v.Patch, other.Patch); c != 0 {
		return c
	}
	return comparePrerelease(v.Pre, other.Pre)
}

func compareUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func comparePrerelease(a, b []string) int {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	// A version without a prerelease has higher precedence.
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdentifier(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

func compareIdentifier(a, b string) int {
	aNum := isNumericIdentifier(a)
	bNum := isNumericIdentifier(b)
	switch {
	case aNum && bNum:
		// Numeric identifiers have no leading zeros, so length ordering is
		// numeric ordering even for values larger than uint64.
		if len(a) != len(b) {
			return compareInt(len(a), len(b))
		}
		return strings.Compare(a, b)
	case aNum:
		return -1 // numeric identifiers sort before alphanumeric ones
	case bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func isNumericIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// IsPrerelease reports whether v has prerelease identifiers.
func (v Version) IsPrerelease() bool { return len(v.Pre) > 0 }

// String returns the canonical version without a leading "v".
func (v Version) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		b.WriteByte('-')
		b.WriteString(strings.Join(v.Pre, "."))
	}
	if v.Build != "" {
		b.WriteByte('+')
		b.WriteString(v.Build)
	}
	return b.String()
}
