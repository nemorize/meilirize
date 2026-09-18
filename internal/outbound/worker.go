package outbound

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"meilirize/internal/blob"
	"meilirize/internal/mailbox"
	"meilirize/internal/provider"
)

const maxStoredErrorRunes = 2048

type Config struct {
	PollInterval  time.Duration
	LeaseDuration time.Duration
	SendTimeout   time.Duration
	BatchSize     int
	MaxAttempts   int
	RetryInitial  time.Duration
	RetryMax      time.Duration
}

type Queue interface {
	ClaimOutboundDeliveries(context.Context, mailbox.ClaimOutboundDeliveriesParams) ([]mailbox.ClaimedSubmission, error)
	RetryOutboundDelivery(context.Context, mailbox.RetryOutboundDeliveryParams) error
	FailOutboundDelivery(context.Context, mailbox.FailOutboundDeliveryParams) error
	CompleteOutboundDelivery(context.Context, mailbox.CompleteOutboundDeliveryParams) (mailbox.SentSubmission, error)
}

type MessageSource interface {
	OpenRaw(context.Context, int64) (io.ReadCloser, error)
}

type Worker struct {
	config    Config
	queue     Queue
	messages  MessageSource
	providers provider.Resolver
	now       func() time.Time
}

func New(
	config Config,
	queue Queue,
	messages MessageSource,
	providers provider.Resolver,
) (*Worker, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if queue == nil {
		return nil, fmt.Errorf("outbound queue must not be nil")
	}
	if messages == nil {
		return nil, fmt.Errorf("outbound message source must not be nil")
	}
	if providers == nil {
		return nil, fmt.Errorf("outbound provider resolver must not be nil")
	}
	return &Worker{
		config:    config,
		queue:     queue,
		messages:  messages,
		providers: providers,
		now:       time.Now,
	}, nil
}

func (worker *Worker) Run(ctx context.Context) error {
	for {
		processed, err := worker.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if processed == worker.config.BatchSize {
			continue
		}

		timer := time.NewTimer(worker.config.PollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
	}
}

func (worker *Worker) RunOnce(ctx context.Context) (int, error) {
	providerNames := worker.providers.Providers()
	if len(providerNames) == 0 {
		return 0, nil
	}
	leaseToken, err := newLeaseToken()
	if err != nil {
		return 0, err
	}
	now := worker.now().UTC()
	claimed, err := worker.queue.ClaimOutboundDeliveries(
		ctx,
		mailbox.ClaimOutboundDeliveriesParams{
			Providers:      providerNames,
			Limit:          worker.config.BatchSize,
			LeaseToken:     leaseToken,
			Now:            now,
			LeaseExpiresAt: now.Add(worker.config.LeaseDuration),
		},
	)
	if err != nil {
		return 0, err
	}

	errorsByJob := make(chan error, len(claimed))
	var group sync.WaitGroup
	for _, submission := range claimed {
		submission := submission
		group.Add(1)
		go func() {
			defer group.Done()
			if err := worker.process(ctx, submission); err != nil {
				errorsByJob <- err
			}
		}()
	}
	group.Wait()
	close(errorsByJob)
	var processingErrors []error
	for err := range errorsByJob {
		processingErrors = append(processingErrors, err)
	}
	return len(claimed), errors.Join(processingErrors...)
}

func (worker *Worker) process(ctx context.Context, submission mailbox.ClaimedSubmission) error {
	delivery := submission.Delivery
	if delivery.AttemptCount > worker.config.MaxAttempts {
		return worker.fail(
			ctx,
			delivery,
			fmt.Errorf("maximum delivery attempts exceeded after lease recovery"),
		)
	}
	sender, ok := worker.providers.Resolve(submission.ProviderBinding.Provider)
	if !ok {
		return worker.fail(
			ctx,
			delivery,
			fmt.Errorf("provider %q is not registered", submission.ProviderBinding.Provider),
		)
	}
	raw, err := worker.messages.OpenRaw(ctx, submission.Message.ID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return worker.handleFailure(ctx, delivery, fmt.Errorf("open message: %w", err))
	}

	sendContext, cancel := context.WithTimeout(ctx, worker.config.SendTimeout)
	result, sendError := sender.Send(sendContext, provider.SendRequest{
		Binding:        submission.ProviderBinding,
		Message:        submission.Message,
		Raw:            raw,
		IdempotencyKey: delivery.IdempotencyKey,
	})
	cancel()
	closeError := raw.Close()
	if sendError == nil && closeError != nil {
		sendError = fmt.Errorf("close message: %w", closeError)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if sendError != nil {
		return worker.handleFailure(ctx, delivery, sendError)
	}
	if strings.TrimSpace(result.MessageID) == "" {
		return worker.fail(ctx, delivery, fmt.Errorf("provider returned an empty message ID"))
	}
	sentAt := result.SentAt
	if sentAt.IsZero() {
		sentAt = worker.now().UTC()
	}
	_, err = worker.queue.CompleteOutboundDelivery(ctx, mailbox.CompleteOutboundDeliveryParams{
		DeliveryID:        delivery.ID,
		LeaseToken:        delivery.LeaseToken,
		ProviderMessageID: result.MessageID,
		SentAt:            sentAt,
	})
	return ignoreLostLease(err)
}

func (worker *Worker) handleFailure(
	ctx context.Context,
	delivery mailbox.OutboundDelivery,
	deliveryError error,
) error {
	if isPermanent(deliveryError) || delivery.AttemptCount >= worker.config.MaxAttempts {
		return worker.fail(ctx, delivery, deliveryError)
	}
	now := worker.now().UTC()
	err := worker.queue.RetryOutboundDelivery(ctx, mailbox.RetryOutboundDeliveryParams{
		DeliveryID:    delivery.ID,
		LeaseToken:    delivery.LeaseToken,
		LastError:     storedError(deliveryError),
		NextAttemptAt: now.Add(worker.retryDelay(delivery.AttemptCount)),
		UpdatedAt:     now,
	})
	return ignoreLostLease(err)
}

func (worker *Worker) fail(
	ctx context.Context,
	delivery mailbox.OutboundDelivery,
	deliveryError error,
) error {
	err := worker.queue.FailOutboundDelivery(ctx, mailbox.FailOutboundDeliveryParams{
		DeliveryID: delivery.ID,
		LeaseToken: delivery.LeaseToken,
		LastError:  storedError(deliveryError),
		UpdatedAt:  worker.now().UTC(),
	})
	return ignoreLostLease(err)
}

func (worker *Worker) retryDelay(attempt int) time.Duration {
	delay := worker.config.RetryInitial
	for current := 1; current < attempt && delay < worker.config.RetryMax; current++ {
		if delay > worker.config.RetryMax/2 {
			return worker.config.RetryMax
		}
		delay *= 2
	}
	if delay > worker.config.RetryMax {
		return worker.config.RetryMax
	}
	return delay
}

func validateConfig(config Config) error {
	if config.PollInterval <= 0 {
		return fmt.Errorf("outbound poll interval must be positive")
	}
	if config.LeaseDuration <= 0 {
		return fmt.Errorf("outbound lease duration must be positive")
	}
	if config.SendTimeout <= 0 {
		return fmt.Errorf("outbound send timeout must be positive")
	}
	if config.SendTimeout >= config.LeaseDuration {
		return fmt.Errorf("outbound send timeout must be shorter than lease duration")
	}
	if config.BatchSize <= 0 || config.BatchSize > 100 {
		return fmt.Errorf("outbound batch size must be between 1 and 100")
	}
	if config.MaxAttempts <= 0 {
		return fmt.Errorf("outbound maximum attempts must be positive")
	}
	if config.RetryInitial <= 0 {
		return fmt.Errorf("outbound initial retry delay must be positive")
	}
	if config.RetryMax < config.RetryInitial {
		return fmt.Errorf("outbound maximum retry delay must not be shorter than initial delay")
	}
	return nil
}

func newLeaseToken() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate outbound lease token: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

func storedError(err error) string {
	if err == nil {
		return "unknown delivery error"
	}
	value := []rune(err.Error())
	if len(value) > maxStoredErrorRunes {
		value = value[:maxStoredErrorRunes]
	}
	return string(value)
}

func isPermanent(err error) bool {
	return provider.IsPermanent(err) ||
		errors.Is(err, blob.ErrCorrupt) ||
		errors.Is(err, blob.ErrNotFound) ||
		errors.Is(err, blob.ErrInvalidKey)
}

func ignoreLostLease(err error) error {
	if errors.Is(err, mailbox.ErrLeaseLost) {
		return nil
	}
	return err
}
