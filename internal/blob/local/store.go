package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"meilirize/internal/blob"
)

const (
	algorithmDirectory = "sha256"
	copyBufferSize     = 32 * 1024
)

var _ blob.Store = (*Store)(nil)

type Store struct {
	root          string
	temporaryRoot string
	mutex         sync.RWMutex
}

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("local blob path must not be empty")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve local blob path %q: %w", root, err)
	}
	temporaryRoot := filepath.Join(absoluteRoot, ".tmp")
	if err := os.MkdirAll(temporaryRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create local blob directory: %w", err)
	}
	return &Store{root: absoluteRoot, temporaryRoot: temporaryRoot}, nil
}

func (store *Store) Put(ctx context.Context, source io.Reader) (reference blob.Ref, resultError error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()

	if source == nil {
		return blob.Ref{}, fmt.Errorf("store local blob: source must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return blob.Ref{}, fmt.Errorf("store local blob: %w", err)
	}

	temporary, err := os.CreateTemp(store.temporaryRoot, "blob-*")
	if err != nil {
		return blob.Ref{}, fmt.Errorf("create temporary blob: %w", err)
	}
	temporaryPath := temporary.Name()
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			if closeError := temporary.Close(); resultError == nil && closeError != nil {
				resultError = fmt.Errorf("close temporary blob: %w", closeError)
			}
		}
		if removeError := os.Remove(temporaryPath); resultError == nil && removeError != nil && !os.IsNotExist(removeError) {
			resultError = fmt.Errorf("remove temporary blob: %w", removeError)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return blob.Ref{}, fmt.Errorf("set temporary blob permissions: %w", err)
	}

	digest := sha256.New()
	size, err := copyWithContext(ctx, io.MultiWriter(temporary, digest), source)
	if err != nil {
		return blob.Ref{}, fmt.Errorf("write local blob: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return blob.Ref{}, fmt.Errorf("sync local blob: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return blob.Ref{}, fmt.Errorf("close local blob: %w", err)
	}
	temporaryOpen = false

	hash := hex.EncodeToString(digest.Sum(nil))
	key := algorithmDirectory + "/" + hash
	reference = blob.Ref{Key: key, SHA256: hash, Size: size}
	destination, err := store.pathForKey(key)
	if err != nil {
		return blob.Ref{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return blob.Ref{}, fmt.Errorf("create local blob shard: %w", err)
	}
	existingError := verifyFile(ctx, destination, reference)
	if existingError == nil {
		return reference, nil
	}
	if !errors.Is(existingError, blob.ErrNotFound) && !errors.Is(existingError, blob.ErrCorrupt) {
		return blob.Ref{}, fmt.Errorf("verify existing local blob: %w", existingError)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		if verificationError := verifyFile(ctx, destination, reference); verificationError != nil {
			return blob.Ref{}, errors.Join(
				fmt.Errorf("commit local blob: %w", err),
				fmt.Errorf("verify concurrently stored local blob: %w", verificationError),
			)
		}
	}
	return reference, nil
}

func (store *Store) Open(ctx context.Context, reference blob.Ref) (io.ReadCloser, error) {
	store.mutex.RLock()
	defer store.mutex.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open local blob: %w", err)
	}
	path, err := store.pathForReference(reference)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("open local blob %q: %w", reference.Key, blob.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("open local blob %q: %w", reference.Key, err)
	}
	return newVerifyingReader(ctx, file, reference), nil
}

func (store *Store) Verify(ctx context.Context, reference blob.Ref) error {
	reader, err := store.Open(ctx, reference)
	if err != nil {
		return err
	}
	_, readError := io.Copy(io.Discard, reader)
	closeError := reader.Close()
	if readError != nil {
		return readError
	}
	return closeError
}

func (store *Store) CollectGarbage(
	ctx context.Context,
	collection blob.GarbageCollection,
) (blob.GarbageCollectionResult, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()

	if collection.DeleteBefore.IsZero() {
		return blob.GarbageCollectionResult{}, fmt.Errorf("collect local blob garbage: cutoff must not be zero")
	}
	root := filepath.Join(store.root, algorithmDirectory)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return blob.GarbageCollectionResult{}, nil
	} else if err != nil {
		return blob.GarbageCollectionResult{}, fmt.Errorf("inspect local blob store: %w", err)
	}

	var result blob.GarbageCollectionResult
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		key, ok := store.keyForPath(path)
		if !ok {
			return nil
		}
		result.Scanned++
		if _, live := collection.LiveKeys[key]; live {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(collection.DeleteBefore) {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		result.Deleted++
		result.DeletedSize += info.Size()
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("collect local blob garbage: %w", err)
	}
	temporaryEntries, err := os.ReadDir(store.temporaryRoot)
	if err != nil {
		return result, fmt.Errorf("read temporary blob directory: %w", err)
	}
	for _, entry := range temporaryEntries {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("collect temporary blob garbage: %w", err)
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "blob-") {
			continue
		}
		result.Scanned++
		info, err := entry.Info()
		if err != nil {
			return result, fmt.Errorf("inspect temporary blob: %w", err)
		}
		if !info.ModTime().Before(collection.DeleteBefore) {
			continue
		}
		if err := os.Remove(filepath.Join(store.temporaryRoot, entry.Name())); err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("remove temporary blob: %w", err)
		}
		result.Deleted++
		result.DeletedSize += info.Size()
	}
	return result, nil
}

func (store *Store) pathForKey(key string) (string, error) {
	prefix := algorithmDirectory + "/"
	if !strings.HasPrefix(key, prefix) {
		return "", fmt.Errorf("%w: %q", blob.ErrInvalidKey, key)
	}
	hash := strings.TrimPrefix(key, prefix)
	if len(hash) != sha256.Size*2 || strings.ToLower(hash) != hash {
		return "", fmt.Errorf("%w: %q", blob.ErrInvalidKey, key)
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("%w: %q", blob.ErrInvalidKey, key)
	}
	return filepath.Join(store.root, algorithmDirectory, hash[:2], hash[2:]), nil
}

func (store *Store) pathForReference(reference blob.Ref) (string, error) {
	path, err := store.pathForKey(reference.Key)
	if err != nil {
		return "", err
	}
	expectedHash := strings.TrimPrefix(reference.Key, algorithmDirectory+"/")
	if reference.SHA256 != expectedHash {
		return "", fmt.Errorf(
			"%w: key %q does not match SHA-256 %q",
			blob.ErrCorrupt,
			reference.Key,
			reference.SHA256,
		)
	}
	if reference.Size < 0 {
		return "", fmt.Errorf("%w: blob %q has a negative size", blob.ErrCorrupt, reference.Key)
	}
	return path, nil
}

func (store *Store) keyForPath(path string) (string, bool) {
	relative, err := filepath.Rel(filepath.Join(store.root, algorithmDirectory), path)
	if err != nil {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != sha256.Size*2-2 {
		return "", false
	}
	hash := parts[0] + parts[1]
	key := algorithmDirectory + "/" + hash
	if _, err := store.pathForKey(key); err != nil {
		return "", false
	}
	return key, true
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, copyBufferSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readError := source.Read(buffer)
		if read > 0 {
			written, writeError := destination.Write(buffer[:read])
			total += int64(written)
			if writeError != nil {
				return total, writeError
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readError, io.EOF) {
			return total, nil
		}
		if readError != nil {
			return total, readError
		}
	}
}

type verifyingReader struct {
	ctx               context.Context
	file              *os.File
	reference         blob.Ref
	digest            hash.Hash
	size              int64
	complete          bool
	closed            bool
	verificationError error
}

func newVerifyingReader(ctx context.Context, file *os.File, reference blob.Ref) *verifyingReader {
	return &verifyingReader{
		ctx:       ctx,
		file:      file,
		reference: reference,
		digest:    sha256.New(),
	}
}

func (reader *verifyingReader) Read(buffer []byte) (int, error) {
	if reader.closed {
		return 0, os.ErrClosed
	}
	if reader.complete {
		if reader.verificationError != nil {
			return 0, reader.verificationError
		}
		return 0, io.EOF
	}
	if err := reader.ctx.Err(); err != nil {
		return 0, fmt.Errorf("read local blob %q: %w", reader.reference.Key, err)
	}

	read, readError := reader.file.Read(buffer)
	if read > 0 {
		_, _ = reader.digest.Write(buffer[:read])
		reader.size += int64(read)
	}
	if errors.Is(readError, io.EOF) {
		reader.complete = true
		reader.verificationError = reader.verify()
		if reader.verificationError != nil {
			return read, reader.verificationError
		}
	}
	if readError != nil && !errors.Is(readError, io.EOF) {
		return read, fmt.Errorf("read local blob %q: %w", reader.reference.Key, readError)
	}
	return read, readError
}

func (reader *verifyingReader) Close() error {
	if reader.closed {
		return nil
	}
	var verificationError error
	if reader.complete {
		verificationError = reader.verificationError
	} else {
		_, verificationError = io.Copy(io.Discard, reader)
	}
	reader.closed = true
	return errors.Join(verificationError, reader.file.Close())
}

func (reader *verifyingReader) verify() error {
	if reader.size != reader.reference.Size {
		return fmt.Errorf(
			"%w: blob %q has size %d, expected %d",
			blob.ErrCorrupt,
			reader.reference.Key,
			reader.size,
			reader.reference.Size,
		)
	}
	actualHash := hex.EncodeToString(reader.digest.Sum(nil))
	if actualHash != reader.reference.SHA256 {
		return fmt.Errorf(
			"%w: blob %q has SHA-256 %q, expected %q",
			blob.ErrCorrupt,
			reader.reference.Key,
			actualHash,
			reader.reference.SHA256,
		)
	}
	return nil
}

func verifyFile(ctx context.Context, path string, reference blob.Ref) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return blob.ErrNotFound
	}
	if err != nil {
		return err
	}
	reader := newVerifyingReader(ctx, file, reference)
	_, readError := io.Copy(io.Discard, reader)
	closeError := reader.Close()
	if readError != nil {
		return readError
	}
	return closeError
}
