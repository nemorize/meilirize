package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	store.mutex.RLock()
	defer store.mutex.RUnlock()

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
	destination, err := store.pathForKey(key)
	if err != nil {
		return blob.Ref{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return blob.Ref{}, fmt.Errorf("create local blob shard: %w", err)
	}
	if _, err := os.Stat(destination); err == nil {
		return blob.Ref{Key: key, SHA256: hash, Size: size}, nil
	} else if !os.IsNotExist(err) {
		return blob.Ref{}, fmt.Errorf("inspect local blob: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		if _, statError := os.Stat(destination); statError != nil {
			return blob.Ref{}, fmt.Errorf("commit local blob: %w", err)
		}
	}
	return blob.Ref{Key: key, SHA256: hash, Size: size}, nil
}

func (store *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	store.mutex.RLock()
	defer store.mutex.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open local blob: %w", err)
	}
	path, err := store.pathForKey(key)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("open local blob %q: %w", key, blob.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("open local blob %q: %w", key, err)
	}
	return file, nil
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
