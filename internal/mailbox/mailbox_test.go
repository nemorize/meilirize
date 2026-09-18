package mailbox

import (
	"errors"
	"testing"
)

func TestNormalizeAddress(t *testing.T) {
	address, err := NormalizeAddress("Alice@Example.COM")
	if err != nil {
		t.Fatal(err)
	}
	if address != "Alice@example.com" {
		t.Fatalf("address = %q", address)
	}
}

func TestNormalizeAddressRejectsDisplayAddress(t *testing.T) {
	_, err := NormalizeAddress("Alice <alice@example.com>")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeProvider(t *testing.T) {
	provider, err := NormalizeProvider("  Mailgun ")
	if err != nil {
		t.Fatal(err)
	}
	if provider != "mailgun" {
		t.Fatalf("provider = %q", provider)
	}
}
