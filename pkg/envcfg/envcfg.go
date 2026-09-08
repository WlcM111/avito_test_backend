// Package envcfg reads configuration from environment variables and collects
// all validation problems instead of failing on the first one.
package envcfg

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Loader accumulates errors while reading environment variables.
type Loader struct {
	errs []string
}

// New returns an empty Loader.
func New() *Loader { return &Loader{} }

// Err returns all accumulated problems as a single error, or nil.
func (l *Loader) Err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration: %s", strings.Join(l.errs, "; "))
}

func (l *Loader) fail(key, msg string) { l.errs = append(l.errs, key+": "+msg) }

// String returns the variable value or def when it is unset or empty.
func (l *Loader) String(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// Required returns the variable value and records an error when it is missing.
func (l *Loader) Required(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		l.fail(key, "is required")
		return ""
	}
	return v
}

// Int returns an integer variable value or def when unset.
func (l *Loader) Int(key string, def int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		l.fail(key, "must be an integer, got "+strconv.Quote(raw))
		return def
	}
	return v
}

// Duration returns a duration variable value or def when unset.
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		l.fail(key, "must be a duration such as 5s, got "+strconv.Quote(raw))
		return def
	}
	return v
}

// Bool returns a boolean variable value or def when unset.
func (l *Loader) Bool(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		l.fail(key, "must be a boolean, got "+strconv.Quote(raw))
		return def
	}
	return v
}

// PositiveInt behaves like Int and additionally requires a value above zero.
func (l *Loader) PositiveInt(key string, def int) int {
	v := l.Int(key, def)
	if v <= 0 {
		l.fail(key, "must be greater than zero")
	}
	return v
}
