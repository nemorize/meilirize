package outbound

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"meilirize/internal/blob"
	"meilirize/internal/mailbox"
	"meilirize/internal/provider"
)

func TestWorkerCompletesSuccessfulDelivery(t *testing.T) {
	now := time.Date(2026, 9, 19, 5, 0, 0, 0, time.UTC)
	queue := &queueStub{claimed: []mailbox.ClaimedSubmission{testClaimedSubmission(1)}}
	messages := &messageSourceStub{raw: "Subject: hello\r\n\r\nbody"}
	registry := provider.NewRegistry()
	if err := registry.Register("example", senderFunc(func(
		_ context.Context,
		request provider.SendRequest,
	) (provider.SendResult, error) {
		contents, err := io.ReadAll(request.Raw)
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != messages.raw {
			t.Fatalf("raw message = %q", contents)
		}
		if request.IdempotencyKey != strings.Repeat("b", 64) {
			t.Fatalf("idempotency key = %q", request.IdempotencyKey)
		}
		return provider.SendResult{MessageID: "provider-1", SentAt: now}, nil
	})); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorker(t, queue, messages, registry, now)

	processed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || len(queue.completed) != 1 {
		t.Fatalf("processed = %d, completed = %#v", processed, queue.completed)
	}
	completion := queue.completed[0]
	if completion.DeliveryID != 1 ||
		completion.LeaseToken == "" ||
		completion.ProviderMessageID != "provider-1" ||
		!completion.SentAt.Equal(now) {
		t.Fatalf("completion = %#v", completion)
	}
	if len(queue.retried) != 0 || len(queue.failed) != 0 {
		t.Fatalf("unexpected transitions: retry=%#v fail=%#v", queue.retried, queue.failed)
	}
	if queue.claimParams.Limit != testWorkerConfig().BatchSize ||
		len(queue.claimParams.LeaseToken) != 64 ||
		!queue.claimParams.LeaseExpiresAt.Equal(now.Add(testWorkerConfig().LeaseDuration)) {
		t.Fatalf("claim params = %#v", queue.claimParams)
	}
}

func TestWorkerSchedulesTransientFailureWithBackoff(t *testing.T) {
	now := time.Date(2026, 9, 19, 5, 0, 0, 0, time.UTC)
	queue := &queueStub{claimed: []mailbox.ClaimedSubmission{testClaimedSubmission(2)}}
	registry := provider.NewRegistry()
	if err := registry.Register("example", senderFunc(func(
		context.Context,
		provider.SendRequest,
	) (provider.SendResult, error) {
		return provider.SendResult{}, errors.New("provider unavailable")
	})); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorker(t, queue, &messageSourceStub{raw: "message"}, registry, now)

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queue.retried) != 1 || len(queue.failed) != 0 {
		t.Fatalf("transitions: retry=%#v fail=%#v", queue.retried, queue.failed)
	}
	retry := queue.retried[0]
	if retry.LastError != "provider unavailable" ||
		!retry.NextAttemptAt.Equal(now.Add(10*time.Second)) {
		t.Fatalf("retry = %#v", retry)
	}
}

func TestWorkerFailsPermanentAndExhaustedDeliveries(t *testing.T) {
	for name, testCase := range map[string]struct {
		attempt int
		err     error
	}{
		"permanent": {attempt: 1, err: provider.Permanent(errors.New("rejected"))},
		"exhausted": {attempt: 3, err: errors.New("still unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 9, 19, 5, 0, 0, 0, time.UTC)
			queue := &queueStub{claimed: []mailbox.ClaimedSubmission{testClaimedSubmission(testCase.attempt)}}
			registry := provider.NewRegistry()
			if err := registry.Register("example", senderFunc(func(
				context.Context,
				provider.SendRequest,
			) (provider.SendResult, error) {
				return provider.SendResult{}, testCase.err
			})); err != nil {
				t.Fatal(err)
			}
			worker := newTestWorker(t, queue, &messageSourceStub{raw: "message"}, registry, now)

			if _, err := worker.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(queue.failed) != 1 || len(queue.retried) != 0 {
				t.Fatalf("transitions: retry=%#v fail=%#v", queue.retried, queue.failed)
			}
		})
	}
}

func TestWorkerTreatsMissingBlobAsPermanent(t *testing.T) {
	now := time.Date(2026, 9, 19, 5, 0, 0, 0, time.UTC)
	queue := &queueStub{claimed: []mailbox.ClaimedSubmission{testClaimedSubmission(1)}}
	registry := provider.NewRegistry()
	if err := registry.Register("example", senderFunc(func(
		context.Context,
		provider.SendRequest,
	) (provider.SendResult, error) {
		t.Fatal("sender must not be called")
		return provider.SendResult{}, nil
	})); err != nil {
		t.Fatal(err)
	}
	worker := newTestWorker(
		t,
		queue,
		&messageSourceStub{err: blob.ErrNotFound},
		registry,
		now,
	)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queue.failed) != 1 || len(queue.retried) != 0 {
		t.Fatalf("transitions: retry=%#v fail=%#v", queue.retried, queue.failed)
	}
}

func TestWorkerWithNoProvidersWaitsUntilCanceled(t *testing.T) {
	worker := newTestWorker(
		t,
		&queueStub{},
		&messageSourceStub{},
		provider.NewRegistry(),
		time.Now().UTC(),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

type queueStub struct {
	claimed     []mailbox.ClaimedSubmission
	claimParams mailbox.ClaimOutboundDeliveriesParams
	retried     []mailbox.RetryOutboundDeliveryParams
	failed      []mailbox.FailOutboundDeliveryParams
	completed   []mailbox.CompleteOutboundDeliveryParams
}

func (queue *queueStub) ClaimOutboundDeliveries(
	_ context.Context,
	params mailbox.ClaimOutboundDeliveriesParams,
) ([]mailbox.ClaimedSubmission, error) {
	queue.claimParams = params
	claimed := queue.claimed
	queue.claimed = nil
	for index := range claimed {
		claimed[index].Delivery.LeaseToken = params.LeaseToken
		claimed[index].Delivery.LeaseExpiresAt = params.LeaseExpiresAt
	}
	return claimed, nil
}

func (queue *queueStub) RetryOutboundDelivery(
	_ context.Context,
	params mailbox.RetryOutboundDeliveryParams,
) error {
	queue.retried = append(queue.retried, params)
	return nil
}

func (queue *queueStub) FailOutboundDelivery(
	_ context.Context,
	params mailbox.FailOutboundDeliveryParams,
) error {
	queue.failed = append(queue.failed, params)
	return nil
}

func (queue *queueStub) CompleteOutboundDelivery(
	_ context.Context,
	params mailbox.CompleteOutboundDeliveryParams,
) (mailbox.SentSubmission, error) {
	queue.completed = append(queue.completed, params)
	return mailbox.SentSubmission{}, nil
}

type messageSourceStub struct {
	raw string
	err error
}

func (source *messageSourceStub) OpenRaw(context.Context, int64) (io.ReadCloser, error) {
	if source.err != nil {
		return nil, source.err
	}
	return io.NopCloser(strings.NewReader(source.raw)), nil
}

type senderFunc func(context.Context, provider.SendRequest) (provider.SendResult, error)

func (send senderFunc) Send(
	ctx context.Context,
	request provider.SendRequest,
) (provider.SendResult, error) {
	return send(ctx, request)
}

func newTestWorker(
	t *testing.T,
	queue Queue,
	messages MessageSource,
	registry provider.Resolver,
	now time.Time,
) *Worker {
	t.Helper()
	worker, err := New(testWorkerConfig(), queue, messages, registry)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return now }
	return worker
}

func testWorkerConfig() Config {
	return Config{
		PollInterval:  time.Millisecond,
		LeaseDuration: time.Minute,
		SendTimeout:   30 * time.Second,
		BatchSize:     4,
		MaxAttempts:   3,
		RetryInitial:  5 * time.Second,
		RetryMax:      time.Minute,
	}
}

func testClaimedSubmission(attempt int) mailbox.ClaimedSubmission {
	return mailbox.ClaimedSubmission{
		Message: mailbox.Message{ID: 10},
		Delivery: mailbox.OutboundDelivery{
			ID:             1,
			MessageID:      10,
			IdempotencyKey: strings.Repeat("b", 64),
			Status:         mailbox.OutboundDeliverySending,
			AttemptCount:   attempt,
		},
		ProviderBinding: mailbox.ProviderBinding{
			ID:       20,
			Provider: "example",
		},
	}
}
