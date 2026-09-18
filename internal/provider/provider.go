package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"meilirize/internal/mailbox"
)

type SendRequest struct {
	Binding        mailbox.ProviderBinding
	Message        mailbox.Message
	Raw            io.Reader
	IdempotencyKey string
}

type SendResult struct {
	MessageID string
	SentAt    time.Time
}

type Sender interface {
	Send(context.Context, SendRequest) (SendResult, error)
}

type Resolver interface {
	Providers() []string
	Resolve(string) (Sender, bool)
}

type Registry struct {
	mutex   sync.RWMutex
	senders map[string]Sender
}

func NewRegistry() *Registry {
	return &Registry{senders: make(map[string]Sender)}
}

func (registry *Registry) Register(name string, sender Sender) error {
	providerName, err := mailbox.NormalizeProvider(name)
	if err != nil {
		return err
	}
	if sender == nil {
		return fmt.Errorf("register provider %q: sender must not be nil", providerName)
	}
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if _, exists := registry.senders[providerName]; exists {
		return fmt.Errorf("register provider %q: %w", providerName, mailbox.ErrConflict)
	}
	registry.senders[providerName] = sender
	return nil
}

func (registry *Registry) Providers() []string {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	providers := make([]string, 0, len(registry.senders))
	for name := range registry.senders {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers
}

func (registry *Registry) Resolve(name string) (Sender, bool) {
	providerName, err := mailbox.NormalizeProvider(name)
	if err != nil {
		return nil, false
	}
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	sender, exists := registry.senders[providerName]
	return sender, exists
}

type permanentError struct {
	err error
}

func (err permanentError) Error() string {
	return err.err.Error()
}

func (err permanentError) Unwrap() error {
	return err.err
}

func Permanent(err error) error {
	if err == nil || IsPermanent(err) {
		return err
	}
	return permanentError{err: err}
}

func IsPermanent(err error) bool {
	var target permanentError
	return errors.As(err, &target)
}

var _ Resolver = (*Registry)(nil)
