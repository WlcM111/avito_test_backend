package domain_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/avito-kitchen/platform/internal/merchant/domain"
)

func TestHashAPIKey(t *testing.T) {
	t.Parallel()
	key := "dev-merchant-key-kafe-primer-0001"

	got := domain.HashAPIKey(key)
	if len(got) != 32 {
		t.Fatalf("hash length = %d, want 32", len(got))
	}
	if !bytes.Equal(got, domain.HashAPIKey(key)) {
		t.Fatal("hash is not deterministic")
	}
	if bytes.Equal(got, domain.HashAPIKey(key+"x")) {
		t.Fatal("different keys produced the same hash")
	}
	if !bytes.Equal(got, domain.HashAPIKey("  "+key+"\n")) {
		t.Fatal("surrounding whitespace changed the hash")
	}
	if bytes.Contains(got, []byte(key)) {
		t.Fatal("hash contains the raw key")
	}
}

func TestValidAPIKeyFormat(t *testing.T) {
	t.Parallel()
	valid := []string{
		"dev-merchant-key-kafe-primer-0001",
		strings.Repeat("k", domain.APIKeyMinLength),
	}
	for _, k := range valid {
		if !domain.ValidAPIKeyFormat(k) {
			t.Fatalf("key %q rejected", k)
		}
	}
	invalid := []string{
		"",
		"short",
		strings.Repeat("k", 257),
		"key with space\tand tab______",
		"ключ-с-кириллицей-длинный-1234",
	}
	for _, k := range invalid {
		if domain.ValidAPIKeyFormat(k) {
			t.Fatalf("key %q accepted", k)
		}
	}
}
