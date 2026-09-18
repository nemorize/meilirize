package config

import (
	"bytes"
	"fmt"
	"os"
	"strconv"

	"github.com/pelletier/go-toml/v2"
)

func Validate(sources Sources) error {
	_, err := Load(sources)
	return err
}

func Load(sources Sources) (Config, error) {
	return load(sources, os.LookupEnv)
}

func load(sources Sources, lookupEnvironment func(string) (string, bool)) (Config, error) {
	configuration := Defaults()

	if sources.HasFile() {
		contents, err := os.ReadFile(sources.FilePath)
		if err != nil {
			return Config{}, fmt.Errorf("read config file %q: %w", sources.FilePath, err)
		}
		decoder := toml.NewDecoder(bytes.NewReader(contents)).DisallowUnknownFields()
		if err := decoder.Decode(&configuration); err != nil {
			return Config{}, fmt.Errorf("parse config file %q: %w", sources.FilePath, err)
		}
	}

	if lookupEnvironment != nil {
		if value, ok := lookupEnvironment(SMTPListenPlainEnvironment); ok {
			configuration.SMTP.ListenPlain = value
		}
		if value, ok := lookupEnvironment(SMTPListenStartTLSEnvironment); ok {
			configuration.SMTP.ListenStartTLS = value
		}
		if value, ok := lookupEnvironment(SMTPListenImplicitEnvironment); ok {
			configuration.SMTP.ListenImplicit = value
		}
		if value, ok := lookupEnvironment(SMTPHostnameEnvironment); ok {
			configuration.SMTP.Hostname = value
		}
		if value, ok := lookupEnvironment(SMTPTLSCertEnvironment); ok {
			configuration.SMTP.TLS.CertFile = value
		}
		if value, ok := lookupEnvironment(SMTPTLSKeyEnvironment); ok {
			configuration.SMTP.TLS.KeyFile = value
		}
		if value, ok := lookupEnvironment(DatabasePathEnvironment); ok {
			configuration.Database.Path = value
		}
		if value, ok := lookupEnvironment(StorageBlobPathEnvironment); ok {
			configuration.Storage.BlobPath = value
		}
		if value, ok := lookupEnvironment(StorageGCIntervalEnvironment); ok {
			configuration.Storage.GC.Interval = value
		}
		if value, ok := lookupEnvironment(StorageGCGraceEnvironment); ok {
			configuration.Storage.GC.GracePeriod = value
		}
		if value, ok := lookupEnvironment(DeliveryPollEnvironment); ok {
			configuration.Delivery.PollInterval = value
		}
		if value, ok := lookupEnvironment(DeliveryLeaseEnvironment); ok {
			configuration.Delivery.LeaseDuration = value
		}
		if value, ok := lookupEnvironment(DeliverySendTimeoutEnvironment); ok {
			configuration.Delivery.SendTimeout = value
		}
		if value, ok := lookupEnvironment(DeliveryBatchSizeEnvironment); ok {
			parsed, err := parseEnvironmentInteger(DeliveryBatchSizeEnvironment, value)
			if err != nil {
				return Config{}, err
			}
			configuration.Delivery.BatchSize = parsed
		}
		if value, ok := lookupEnvironment(DeliveryMaxAttemptsEnvironment); ok {
			parsed, err := parseEnvironmentInteger(DeliveryMaxAttemptsEnvironment, value)
			if err != nil {
				return Config{}, err
			}
			configuration.Delivery.MaxAttempts = parsed
		}
		if value, ok := lookupEnvironment(DeliveryRetryInitialEnvironment); ok {
			configuration.Delivery.RetryInitial = value
		}
		if value, ok := lookupEnvironment(DeliveryRetryMaxEnvironment); ok {
			configuration.Delivery.RetryMax = value
		}
	}

	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}
	return configuration, nil
}

func parseEnvironmentInteger(name string, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("environment variable %s must be an integer: %w", name, err)
	}
	return parsed, nil
}
