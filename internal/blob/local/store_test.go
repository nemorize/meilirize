package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	reader, err := store.Open(context.Background(), first)
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

func TestPutRepairsCorruptExistingBlob(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	contents := "original message"
	reference, err := store.Put(ctx, strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.pathForKey(reference.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", len(contents))), 0o600); err != nil {
		t.Fatal(err)
	}

	repaired, err := store.Put(ctx, strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	if repaired != reference {
		t.Fatalf("repaired ref = %#v, want %#v", repaired, reference)
	}
	if err := store.Verify(ctx, reference); err != nil {
		t.Fatalf("verify repaired blob: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != contents {
		t.Fatalf("repaired contents = %q", got)
	}
}

func TestOpenDetectsCorruptContentAndReference(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	reference, err := store.Put(ctx, strings.NewReader("message"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.pathForKey(reference.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	reader, err := store.Open(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(reader); !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("read corrupt blob error = %v", err)
	}
	if err := reader.Close(); !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("close corrupt blob error = %v", err)
	}
	reader, err = store.Open(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("close unread corrupt blob error = %v", err)
	}
	if err := store.Verify(ctx, reference); !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("verify corrupt blob error = %v", err)
	}
	if _, err := store.Put(ctx, strings.NewReader("message")); err != nil {
		t.Fatal(err)
	}

	wrongSize := reference
	wrongSize.Size++
	if err := store.Verify(ctx, wrongSize); !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("verify wrong-size reference error = %v", err)
	}
	wrongHash := reference
	wrongHash.SHA256 = strings.Repeat("0", 64)
	if _, err := store.Open(ctx, wrongHash); !errors.Is(err, blob.ErrCorrupt) {
		t.Fatalf("open mismatched reference error = %v", err)
	}
}

func TestStoreRejectsInvalidKeys(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "../message", "sha256/../message", "sha256/not-a-hash"} {
		reference := blob.Ref{Key: key, SHA256: strings.TrimPrefix(key, "sha256/")}
		if _, err := store.Open(context.Background(), reference); !errors.Is(err, blob.ErrInvalidKey) {
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
	reference := blob.Ref{
		Key:    "sha256/" + strings.Repeat("0", 64),
		SHA256: strings.Repeat("0", 64),
	}
	if _, err := store.Open(ctx, reference); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open error = %v", err)
	}
}

func TestStoreCollectsOnlyOldUnreferencedBlobs(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	live, err := store.Put(ctx, strings.NewReader("live"))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := store.Put(ctx, strings.NewReader("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	recent, err := store.Put(ctx, strings.NewReader("recent"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, reference := range []blob.Ref{live, orphan} {
		path, err := store.pathForKey(reference.Key)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	temporaryPath := filepath.Join(store.temporaryRoot, "blob-abandoned")
	if err := os.WriteFile(temporaryPath, []byte("temporary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(temporaryPath, old, old); err != nil {
		t.Fatal(err)
	}

	result, err := store.CollectGarbage(ctx, blob.GarbageCollection{
		LiveKeys:     map[string]struct{}{live.Key: {}},
		DeleteBefore: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 4 || result.Deleted != 2 || result.DeletedSize != orphan.Size+int64(len("temporary")) {
		t.Fatalf("garbage collection result = %#v", result)
	}
	if reader, err := store.Open(ctx, live); err != nil {
		t.Fatal(err)
	} else {
		_ = reader.Close()
	}
	if _, err := store.Open(ctx, orphan); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("orphan open error = %v", err)
	}
	if reader, err := store.Open(ctx, recent); err != nil {
		t.Fatal(err)
	} else {
		_ = reader.Close()
	}
}
