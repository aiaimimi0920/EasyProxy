package naming

import (
	"strings"
	"testing"
)

func TestNormalizeIsStableAndBounded(t *testing.T) {
	input := strings.Repeat("Very Long Name ", 10)
	first := Normalize(input)
	second := Normalize(input)
	if first != second {
		t.Fatalf("Normalize() is not deterministic: %q != %q", first, second)
	}
	if len(first) > maxResourceNameLength {
		t.Fatalf("Normalize() length = %d", len(first))
	}
	if !strings.HasSuffix(first, "-b3a3a8cb37") {
		t.Fatalf("Normalize() = %q, want stable hash suffix", first)
	}
}

func TestNormalizeCleansSeparators(t *testing.T) {
	if got := Normalize("  My_Project / Test  "); got != "my-project-test" {
		t.Fatalf("Normalize() = %q", got)
	}
}
