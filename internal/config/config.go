package config

import (
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	SMTPListenPlainEnvironment      = EnvironmentPrefix + "SMTP_LISTEN_PLAIN"
	SMTPListenStartTLSEnvironment   = EnvironmentPrefix + "SMTP_LISTEN_STARTTLS"
	SMTPListenImplicitEnvironment   = EnvironmentPrefix + "SMTP_LISTEN_IMPLICIT"
	SMTPHostnameEnvironment         = EnvironmentPrefix + "SMTP_HOSTNAME"
	SMTPTLSCertEnvironment          = EnvironmentPrefix + "SMTP_TLS_CERT_FILE"
	SMTPTLSKeyEnvironment           = EnvironmentPrefix + "SMTP_TLS_KEY_FILE"
	DatabasePathEnvironment         = EnvironmentPrefix + "DATABASE_PATH"
	StorageBlobPathEnvironment      = EnvironmentPrefix + "STORAGE_BLOB_PATH"
	StorageGCIntervalEnvironment    = EnvironmentPrefix + "STORAGE_GC_INTERVAL"
	StorageGCGraceEnvironment       = EnvironmentPrefix + "STORAGE_GC_GRACE_PERIOD"
	DeliveryPollEnvironment         = EnvironmentPrefix + "DELIVERY_POLL_INTERVAL"
	DeliveryLeaseEnvironment        = EnvironmentPrefix + "DELIVERY_LEASE_DURATION"
	DeliverySendTimeoutEnvironment  = EnvironmentPrefix + "DELIVERY_SEND_TIMEOUT"
	DeliveryBatchSizeEnvironment    = EnvironmentPrefix + "DELIVERY_BATCH_SIZE"
	DeliveryMaxAttemptsEnvironment  = EnvironmentPrefix + "DELIVERY_MAX_ATTEMPTS"
	DeliveryRetryInitialEnvironment = EnvironmentPrefix + "DELIVERY_RETRY_INITIAL"
	DeliveryRetryMaxEnvironment     = EnvironmentPrefix + "DELIVERY_RETRY_MAX"

	SMTPModePlain    = "plain"
	SMTPModeStartTLS = "starttls"
	SMTPModeImplicit = "implicit"
)

type Config struct {
	SMTP     SMTPConfig     `toml:"smtp"`
	Database DatabaseConfig `toml:"database"`
	Storage  StorageConfig  `toml:"storage"`
	Delivery DeliveryConfig `toml:"delivery"`
}

type SMTPConfig struct {
	ListenPlain    string        `toml:"listen_plain"`
	ListenStartTLS string        `toml:"listen_starttls"`
	ListenImplicit string        `toml:"listen_implicit"`
	Hostname       string        `toml:"hostname"`
	TLS            SMTPTLSConfig `toml:"tls"`
}

type SMTPListenerConfig struct {
	Address string
	Mode    string
}

type SMTPTLSConfig struct {
	CertFile string `toml:"cert_file"`
	KeyFile  string `toml:"key_file"`
}

type DatabaseConfig struct {
	Path string `toml:"path"`
}

type StorageConfig struct {
	BlobPath string          `toml:"blob_path"`
	GC       StorageGCConfig `toml:"gc"`
}

type StorageGCConfig struct {
	Interval    string `toml:"interval"`
	GracePeriod string `toml:"grace_period"`
}

type DeliveryConfig struct {
	PollInterval  string `toml:"poll_interval"`
	LeaseDuration string `toml:"lease_duration"`
	SendTimeout   string `toml:"send_timeout"`
	BatchSize     int    `toml:"batch_size"`
	MaxAttempts   int    `toml:"max_attempts"`
	RetryInitial  string `toml:"retry_initial"`
	RetryMax      string `toml:"retry_max"`
}

type DeliveryRuntimeConfig struct {
	PollInterval  time.Duration
	LeaseDuration time.Duration
	SendTimeout   time.Duration
	BatchSize     int
	MaxAttempts   int
	RetryInitial  time.Duration
	RetryMax      time.Duration
}

func Defaults() Config {
	return Config{
		SMTP: SMTPConfig{
			ListenPlain: "127.0.0.1:2525",
			Hostname:    "localhost",
		},
		Database: DatabaseConfig{
			Path: "meilirize.db",
		},
		Storage: StorageConfig{
			BlobPath: "meilirize-data/blobs",
			GC: StorageGCConfig{
				Interval:    "24h",
				GracePeriod: "24h",
			},
		},
		Delivery: DeliveryConfig{
			PollInterval:  "1s",
			LeaseDuration: "1m",
			SendTimeout:   "30s",
			BatchSize:     8,
			MaxAttempts:   5,
			RetryInitial:  "5s",
			RetryMax:      "15m",
		},
	}
}

func (configuration Config) Validate() error {
	hostname := configuration.SMTP.Hostname
	if hostname == "" {
		return fmt.Errorf("smtp.hostname must not be empty")
	}
	if strings.ContainsAny(hostname, " \t\r\n") {
		return fmt.Errorf("smtp.hostname must not contain whitespace")
	}
	if strings.TrimSpace(configuration.Database.Path) == "" {
		return fmt.Errorf("database.path must not be empty")
	}
	if strings.TrimSpace(configuration.Storage.BlobPath) == "" {
		return fmt.Errorf("storage.blob_path must not be empty")
	}
	if _, _, err := configuration.Storage.GarbageCollectionDurations(); err != nil {
		return err
	}
	if _, err := configuration.Delivery.Runtime(); err != nil {
		return err
	}

	listeners, err := configuration.SMTP.ResolvedListeners()
	if err != nil {
		return err
	}

	for _, listener := range listeners {
		if listener.Mode == SMTPModePlain {
			continue
		}
		if configuration.SMTP.TLS.CertFile == "" {
			return fmt.Errorf("smtp.tls.cert_file must not be empty when TLS is enabled")
		}
		if configuration.SMTP.TLS.KeyFile == "" {
			return fmt.Errorf("smtp.tls.key_file must not be empty when TLS is enabled")
		}
		break
	}
	return nil
}

func (configuration DeliveryConfig) Runtime() (DeliveryRuntimeConfig, error) {
	pollInterval, err := positiveDuration("delivery.poll_interval", configuration.PollInterval)
	if err != nil {
		return DeliveryRuntimeConfig{}, err
	}
	leaseDuration, err := positiveDuration("delivery.lease_duration", configuration.LeaseDuration)
	if err != nil {
		return DeliveryRuntimeConfig{}, err
	}
	sendTimeout, err := positiveDuration("delivery.send_timeout", configuration.SendTimeout)
	if err != nil {
		return DeliveryRuntimeConfig{}, err
	}
	if sendTimeout >= leaseDuration {
		return DeliveryRuntimeConfig{}, fmt.Errorf("delivery.send_timeout must be shorter than delivery.lease_duration")
	}
	if configuration.BatchSize <= 0 || configuration.BatchSize > 100 {
		return DeliveryRuntimeConfig{}, fmt.Errorf("delivery.batch_size must be between 1 and 100")
	}
	if configuration.MaxAttempts <= 0 {
		return DeliveryRuntimeConfig{}, fmt.Errorf("delivery.max_attempts must be positive")
	}
	retryInitial, err := positiveDuration("delivery.retry_initial", configuration.RetryInitial)
	if err != nil {
		return DeliveryRuntimeConfig{}, err
	}
	retryMax, err := positiveDuration("delivery.retry_max", configuration.RetryMax)
	if err != nil {
		return DeliveryRuntimeConfig{}, err
	}
	if retryMax < retryInitial {
		return DeliveryRuntimeConfig{}, fmt.Errorf("delivery.retry_max must not be shorter than delivery.retry_initial")
	}
	return DeliveryRuntimeConfig{
		PollInterval:  pollInterval,
		LeaseDuration: leaseDuration,
		SendTimeout:   sendTimeout,
		BatchSize:     configuration.BatchSize,
		MaxAttempts:   configuration.MaxAttempts,
		RetryInitial:  retryInitial,
		RetryMax:      retryMax,
	}, nil
}

func positiveDuration(name string, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return duration, nil
}

func (configuration StorageConfig) GarbageCollectionDurations() (time.Duration, time.Duration, error) {
	interval, err := time.ParseDuration(strings.TrimSpace(configuration.GC.Interval))
	if err != nil {
		return 0, 0, fmt.Errorf("storage.gc.interval must be a duration: %w", err)
	}
	if interval <= 0 {
		return 0, 0, fmt.Errorf("storage.gc.interval must be positive")
	}
	gracePeriod, err := time.ParseDuration(strings.TrimSpace(configuration.GC.GracePeriod))
	if err != nil {
		return 0, 0, fmt.Errorf("storage.gc.grace_period must be a duration: %w", err)
	}
	if gracePeriod < 0 {
		return 0, 0, fmt.Errorf("storage.gc.grace_period must not be negative")
	}
	return interval, gracePeriod, nil
}

func (configuration SMTPConfig) ResolvedListeners() ([]SMTPListenerConfig, error) {
	candidates := []struct {
		address string
		name    string
		mode    string
	}{
		{configuration.ListenPlain, "smtp.listen_plain", SMTPModePlain},
		{configuration.ListenStartTLS, "smtp.listen_starttls", SMTPModeStartTLS},
		{configuration.ListenImplicit, "smtp.listen_implicit", SMTPModeImplicit},
	}
	listeners := make([]SMTPListenerConfig, 0, len(candidates))
	seenAddresses := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.address == "" {
			continue
		}
		address := candidate.address
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("%s must be a host:port address: %w", candidate.name, err)
		}
		if _, ok := seenAddresses[address]; ok && port != "0" {
			return nil, fmt.Errorf("%s duplicates enabled listener address %q", candidate.name, address)
		}
		seenAddresses[address] = struct{}{}
		listeners = append(listeners, SMTPListenerConfig{Address: address, Mode: candidate.mode})
	}
	if len(listeners) == 0 {
		return nil, fmt.Errorf("at least one SMTP listener must be configured")
	}
	return listeners, nil
}
