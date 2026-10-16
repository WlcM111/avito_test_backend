package envcfg_test

import (
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/pkg/envcfg"
)

// Unset and empty variables fall back to the default; a set one wins.
func TestDefaultsAndValues(t *testing.T) {
	t.Setenv("ENVCFG_STRING", "value")
	t.Setenv("ENVCFG_EMPTY", "")
	t.Setenv("ENVCFG_INT", "42")
	t.Setenv("ENVCFG_DURATION", "1m30s")
	t.Setenv("ENVCFG_BOOL", "true")

	l := envcfg.New()
	if got := l.String("ENVCFG_STRING", "def"); got != "value" {
		t.Fatalf("String = %q, want value", got)
	}
	if got := l.String("ENVCFG_EMPTY", "def"); got != "def" {
		t.Fatalf("String(empty) = %q, want the default", got)
	}
	if got := l.String("ENVCFG_UNSET", "def"); got != "def" {
		t.Fatalf("String(unset) = %q, want the default", got)
	}
	if got := l.Int("ENVCFG_INT", 1); got != 42 {
		t.Fatalf("Int = %d, want 42", got)
	}
	if got := l.Int("ENVCFG_UNSET", 7); got != 7 {
		t.Fatalf("Int(unset) = %d, want 7", got)
	}
	if got := l.Duration("ENVCFG_DURATION", time.Second); got != 90*time.Second {
		t.Fatalf("Duration = %s, want 1m30s", got)
	}
	if got := l.Duration("ENVCFG_UNSET", time.Second); got != time.Second {
		t.Fatalf("Duration(unset) = %s, want 1s", got)
	}
	if got := l.Bool("ENVCFG_BOOL", false); !got {
		t.Fatal("Bool = false, want true")
	}
	if got := l.Bool("ENVCFG_UNSET", true); !got {
		t.Fatal("Bool(unset) = false, want the default true")
	}
	if got := l.PositiveInt("ENVCFG_INT", 1); got != 42 {
		t.Fatalf("PositiveInt = %d, want 42", got)
	}
	if err := l.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
}

// Every problem is collected and reported at once, with the variable named,
// so a misconfigured service tells everything that is wrong in one start.
func TestAllProblemsAreReportedTogether(t *testing.T) {
	t.Setenv("ENVCFG_BAD_INT", "ten")
	t.Setenv("ENVCFG_BAD_DURATION", "5")
	t.Setenv("ENVCFG_BAD_BOOL", "maybe")
	t.Setenv("ENVCFG_ZERO", "0")

	l := envcfg.New()
	if got := l.Int("ENVCFG_BAD_INT", 3); got != 3 {
		t.Fatalf("a malformed Int must fall back to the default, got %d", got)
	}
	if got := l.Duration("ENVCFG_BAD_DURATION", time.Second); got != time.Second {
		t.Fatalf("a malformed Duration must fall back to the default, got %s", got)
	}
	if got := l.Bool("ENVCFG_BAD_BOOL", true); !got {
		t.Fatal("a malformed Bool must fall back to the default")
	}
	l.PositiveInt("ENVCFG_ZERO", 5)
	if got := l.Required("ENVCFG_MISSING"); got != "" {
		t.Fatalf("Required(missing) = %q, want empty", got)
	}

	err := l.Err()
	if err == nil {
		t.Fatal("Err() = nil, want the collected problems")
	}
	for _, want := range []string{
		"ENVCFG_BAD_INT: must be an integer",
		"ENVCFG_BAD_DURATION: must be a duration",
		"ENVCFG_BAD_BOOL: must be a boolean",
		"ENVCFG_ZERO: must be greater than zero",
		"ENVCFG_MISSING: is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Err() = %q, want it to mention %q", err, want)
		}
	}
}

func TestRequiredReturnsTheValue(t *testing.T) {
	t.Setenv("ENVCFG_REQUIRED", "postgres://localhost/db")
	l := envcfg.New()
	if got := l.Required("ENVCFG_REQUIRED"); got != "postgres://localhost/db" {
		t.Fatalf("Required = %q", got)
	}
	t.Setenv("ENVCFG_REQUIRED_EMPTY", "")
	l.Required("ENVCFG_REQUIRED_EMPTY")
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "ENVCFG_REQUIRED_EMPTY") {
		t.Fatalf("an empty required variable must be reported, got %v", err)
	}
}
