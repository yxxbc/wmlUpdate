package version

import "testing"

func TestParse(t *testing.T) {
	t.Parallel()
	valid := []string{"0.0.0", "1.2.3", "v1.2.3", "1.2.3-alpha", "1.2.3-alpha.1", "1.2.3-0.3.7", "1.2.3+build.1", "1.2.3-rc.1+sha.abc"}
	for _, s := range valid {
		if _, err := Parse(s); err != nil {
			t.Errorf("Parse(%q) error: %v", s, err)
		}
	}
	invalid := []string{"", "v", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03", "1.2.3-", "1.2.3-01", "1.2.3+", "1.2.3+foo..bar", "1.2.3-α", " 1.2.3 break"}
	for _, s := range invalid {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", s)
		}
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	ordered := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
		"1.0.1",
		"1.1.0",
		"2.0.0",
	}
	for i := 0; i < len(ordered)-1; i++ {
		a := MustParse(ordered[i])
		b := MustParse(ordered[i+1])
		if a.Compare(b) >= 0 {
			t.Errorf("%s should be less than %s", a, b)
		}
		if b.Compare(a) <= 0 {
			t.Errorf("%s should be greater than %s", b, a)
		}
	}
	if MustParse("1.0.0+one").Compare(MustParse("1.0.0+two")) != 0 {
		t.Error("build metadata must be ignored")
	}
	if MustParse("1.0.0-999999999999999999999999999999").Compare(MustParse("1.0.0-1000000000000000000000000000000")) >= 0 {
		t.Error("large numeric prerelease identifiers should compare numerically")
	}
}

func TestString(t *testing.T) {
	t.Parallel()
	v := MustParse("v1.2.3-rc.1+build.5")
	if got, want := v.String(), "1.2.3-rc.1+build.5"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
