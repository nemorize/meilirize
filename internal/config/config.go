package config

import (
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	SMTPListenPlainEnvironment    = EnvironmentPrefix + "SMTP_LISTEN_PLAIN"
	SMTPListenStartTLSEnvironment = EnvironmentPrefix + "SMTP_LISTEN_STARTTLS"
	SMTPListenImplicitEnvironment = EnvironmentPrefix + "SMTP_LISTEN_IMPLICIT"
	SMTPHostnameEnvironment       = EnvironmentPrefix + "SMTP_HOSTNAME"
	SMTPTLSCertEnvironment        = EnvironmentPrefix + "SMTP_TLS_CERT_FILE"
	SMTPTLSKeyEnvironment         = EnvironmentPrefix + "SMTP_TLS_KEY_FILE"
	DatabasePathEnvironment       = EnvironmentPrefix + "DATABASE_PATH"
	StorageBlobPathEnvironment    = EnvironmentPrefix + "STORAGE_BLOB_PATH"
	StorageGCIntervalEnvironment  = EnvironmentPrefix + "STORAGE_GC_INTERVAL"
	StorageGCGraceEnvironment     = EnvironmentPrefix + "STORAGE_GC_GRACE_PERIOD"

	SMTPModePlain    = "plain"
	SMTPModeStartTLS = "starttls"
	SMTPModeImplicit = "implicit"
)

type Config struct {
	SMTP     SMTPConfig     `toml:"smtp"`
	Database DatabaseConfig `toml:"database"`
	Storage  StorageConfig  `toml:"storage"`
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
