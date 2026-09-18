package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRegistryNormalizesAndOrdersProviders(t *testing.T) {
	registry := NewRegistry()
	sender := senderStub{}
	if err := registry.Register(" Zeta ", sender); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("alpha", sender); err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "zeta"}; !reflect.DeepEqual(registry.Providers(), want) {
		t.Fatalf("providers = %v, want %v", registry.Providers(), want)
	}
	if resolved, ok := registry.Resolve("ZETA"); !ok || resolved == nil {
		t.Fatal("normalized provider was not resolved")
	}
	if err := registry.Register("zeta", sender); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestPermanentErrorClassification(t *testing.T) {
	source := errors.New("rejected")
	marked := Permanent(source)
	if !IsPermanent(marked) || !errors.Is(marked, source) {
		t.Fatalf("permanent error = %v", marked)
	}
	if IsPermanent(source) {
		t.Fatal("ordinary error classified as permanent")
	}
}

type senderStub struct{}

func (senderStub) Send(context.Context, SendRequest) (SendResult, error) {
	return SendResult{}, nil
}
