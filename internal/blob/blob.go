package blob

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrInvalidKey = errors.New("invalid blob key")
	ErrNotFound   = errors.New("blob not found")
)

type Ref struct {
	Key    string
	SHA256 string
	Size   int64
}

type GarbageCollection struct {
	LiveKeys     map[string]struct{}
	DeleteBefore time.Time
}

type GarbageCollectionResult struct {
	Scanned     int
	Deleted     int
	DeletedSize int64
}

type Store interface {
	Put(context.Context, io.Reader) (Ref, error)
	Open(context.Context, string) (io.ReadCloser, error)
	CollectGarbage(context.Context, GarbageCollection) (GarbageCollectionResult, error)
}
