package local

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"meilirize/internal/blob"
)

func TestStoreRoundTripAndDeduplication(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	contents := "From: alice@example.com\r\n\r\nhello\r\n"

	first, err := store.Put(context.Background(), strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(context.Background(), strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("blob refs differ: %#v != %#v", first, second)
	}
	if first.Size != int64(len(contents)) || first.SHA256 == "" {
		t.Fatalf("unexpected blob ref: %#v", first)
	}

	reader, err := store.Open(context.Background(), first.Key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if closeError := reader.Close(); err == nil {
		err = closeError
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != contents {
		t.Fatalf("contents = %q", got)
	}

	entries, err := os.ReadDir(store.temporaryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary entries = %d", len(entries))
	}
}

func TestStoreRejectsInvalidKeys(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "../message", "sha256/../message", "sha256/not-a-hash"} {
		if _, err := store.Open(context.Background(), key); !errors.Is(err, blob.ErrInvalidKey) {
			t.Fatalf("Open(%q) error = %v", key, err)
		}
	}
}

func TestStoreHonorsCanceledContext(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Put(ctx, strings.NewReader("message")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put error = %v", err)
	}
	if _, err := store.Open(ctx, "sha256/"+strings.Repeat("0", 64)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open error = %v", err)
	}
}
