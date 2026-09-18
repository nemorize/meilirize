package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadUsesDefaultsWithoutOtherSources(t *testing.T) {
	configuration, err := load(newSources("", FileNotSelected), noEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(configuration, Defaults()) {
		t.Fatalf("configuration = %+v, want %+v", configuration, Defaults())
	}
}

func TestLoadUsesDefaultsForEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "")

	configuration, err := load(newSources(path, FileSelectedByFlag), noEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(configuration, Defaults()) {
		t.Fatalf("configuration = %+v, want %+v", configuration, Defaults())
	}
}

func TestLoadMultipleSMTPListeners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, `[smtp]
hostname = "mail.example"
listen_plain = "127.0.0.1:2525"
listen_starttls = "127.0.0.1:2587"
listen_implicit = "127.0.0.1:2465"

[smtp.tls]
cert_file = "cert.pem"
key_file = "key.pem"
`)

	configuration, err := load(newSources(path, FileSelectedByFlag), noEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	listeners, err := configuration.SMTP.ResolvedListeners()
	if err != nil {
		t.Fatal(err)
	}
	want := []SMTPListenerConfig{
		{Address: "127.0.0.1:2525", Mode: SMTPModePlain},
		{Address: "127.0.0.1:2587", Mode: SMTPModeStartTLS},
		{Address: "127.0.0.1:2465", Mode: SMTPModeImplicit},
	}
	if !reflect.DeepEqual(listeners, want) {
		t.Fatalf("SMTP listeners = %+v, want %+v", listeners, want)
	}
}

func TestLoadAppliesFileThenEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, `[smtp]
listen_plain = "127.0.0.1:2500"
listen_starttls = "127.0.0.1:2580"
listen_implicit = "127.0.0.1:2460"
hostname = "file.example"

[smtp.tls]
cert_file = "file-cert.pem"
key_file = "file-key.pem"

[database]
path = "file.db"

[storage]
blob_path = "file-blobs"

[storage.gc]
interval = "12h"
grace_period = "6h"

[delivery]
poll_interval = "2s"
lease_duration = "2m"
send_timeout = "45s"
batch_size = 4
max_attempts = 6
retry_initial = "10s"
retry_max = "20m"
`)
	sources := newSources(path, FileSelectedByFlag)
	environment := map[string]string{
		SMTPListenPlainEnvironment:      "127.0.0.1:2600",
		SMTPListenStartTLSEnvironment:   "127.0.0.1:2680",
		SMTPListenImplicitEnvironment:   "127.0.0.1:2468",
		SMTPHostnameEnvironment:         "env.example",
		SMTPTLSCertEnvironment:          "env-cert.pem",
		SMTPTLSKeyEnvironment:           "env-key.pem",
		DatabasePathEnvironment:         "env.db",
		StorageBlobPathEnvironment:      "env-blobs",
		StorageGCIntervalEnvironment:    "8h",
		StorageGCGraceEnvironment:       "4h",
		DeliveryPollEnvironment:         "3s",
		DeliveryLeaseEnvironment:        "3m",
		DeliverySendTimeoutEnvironment:  "1m",
		DeliveryBatchSizeEnvironment:    "12",
		DeliveryMaxAttemptsEnvironment:  "7",
		DeliveryRetryInitialEnvironment: "15s",
		DeliveryRetryMaxEnvironment:     "30m",
	}

	configuration, err := load(sources, func(name string) (string, bool) {
		value, ok := environment[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.SMTP.ListenPlain != "127.0.0.1:2600" {
		t.Fatalf("smtp plain listen = %q", configuration.SMTP.ListenPlain)
	}
	if configuration.SMTP.ListenStartTLS != "127.0.0.1:2680" {
		t.Fatalf("smtp STARTTLS listen = %q", configuration.SMTP.ListenStartTLS)
	}
	if configuration.SMTP.ListenImplicit != "127.0.0.1:2468" {
		t.Fatalf("smtp implicit TLS listen = %q", configuration.SMTP.ListenImplicit)
	}
	if configuration.SMTP.Hostname != "env.example" {
		t.Fatalf("smtp hostname = %q", configuration.SMTP.Hostname)
	}
	if configuration.SMTP.TLS.CertFile != "env-cert.pem" {
		t.Fatalf("smtp TLS certificate file = %q", configuration.SMTP.TLS.CertFile)
	}
	if configuration.SMTP.TLS.KeyFile != "env-key.pem" {
		t.Fatalf("smtp TLS key file = %q", configuration.SMTP.TLS.KeyFile)
	}
	if configuration.Database.Path != "env.db" {
		t.Fatalf("database path = %q", configuration.Database.Path)
	}
	if configuration.Storage.BlobPath != "env-blobs" {
		t.Fatalf("storage blob path = %q", configuration.Storage.BlobPath)
	}
	gcInterval, gcGracePeriod, err := configuration.Storage.GarbageCollectionDurations()
	if err != nil {
		t.Fatal(err)
	}
	if gcInterval != 8*time.Hour || gcGracePeriod != 4*time.Hour {
		t.Fatalf("storage GC durations = %s, %s", gcInterval, gcGracePeriod)
	}
	delivery, err := configuration.Delivery.Runtime()
	if err != nil {
		t.Fatal(err)
	}
	if delivery.PollInterval != 3*time.Second ||
		delivery.LeaseDuration != 3*time.Minute ||
		delivery.SendTimeout != time.Minute ||
		delivery.BatchSize != 12 ||
		delivery.MaxAttempts != 7 ||
		delivery.RetryInitial != 15*time.Second ||
		delivery.RetryMax != 30*time.Minute {
		t.Fatalf("delivery configuration = %#v", delivery)
	}
}

func TestLoadSelectsSMTPListenersFromEnvironment(t *testing.T) {
	environment := map[string]string{
		SMTPListenPlainEnvironment:    "",
		SMTPListenStartTLSEnvironment: "127.0.0.1:2587",
		SMTPListenImplicitEnvironment: "127.0.0.1:2465",
		SMTPTLSCertEnvironment:        "cert.pem",
		SMTPTLSKeyEnvironment:         "key.pem",
	}
	configuration, err := load(newSources("", FileNotSelected), func(name string) (string, bool) {
		value, ok := environment[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	listeners, err := configuration.SMTP.ResolvedListeners()
	if err != nil {
		t.Fatal(err)
	}
	want := []SMTPListenerConfig{
		{Address: "127.0.0.1:2587", Mode: SMTPModeStartTLS},
		{Address: "127.0.0.1:2465", Mode: SMTPModeImplicit},
	}
	if !reflect.DeepEqual(listeners, want) {
		t.Fatalf("SMTP listeners = %+v, want %+v", listeners, want)
	}
}

func TestLoadRejectsUnknownFileSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[smtp]\nunknown = true\n")

	_, err := load(newSources(path, FileSelectedByFlag), noEnvironment)
	if err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	for name, environment := range map[string]map[string]string{
		"plain listen": {
			SMTPListenPlainEnvironment: "2525",
		},
		"hostname": {
			SMTPHostnameEnvironment: "mail.example\r\ninjected",
		},
		"no listeners": {
			SMTPListenPlainEnvironment: "",
		},
		"database path": {
			DatabasePathEnvironment: "",
		},
		"storage blob path": {
			StorageBlobPathEnvironment: "",
		},
		"empty GC interval": {
			StorageGCIntervalEnvironment: "",
		},
		"invalid GC interval": {
			StorageGCIntervalEnvironment: "daily",
		},
		"zero GC interval": {
			StorageGCIntervalEnvironment: "0s",
		},
		"negative GC grace period": {
			StorageGCGraceEnvironment: "-1h",
		},
		"invalid delivery poll interval": {
			DeliveryPollEnvironment: "often",
		},
		"delivery timeout exceeds lease": {
			DeliveryLeaseEnvironment:       "30s",
			DeliverySendTimeoutEnvironment: "30s",
		},
		"invalid delivery batch size": {
			DeliveryBatchSizeEnvironment: "many",
		},
		"zero delivery attempts": {
			DeliveryMaxAttemptsEnvironment: "0",
		},
		"delivery retry range": {
			DeliveryRetryInitialEnvironment: "2m",
			DeliveryRetryMaxEnvironment:     "1m",
		},
		"STARTTLS listen": {
			SMTPListenStartTLSEnvironment: "2587",
			SMTPTLSCertEnvironment:        "cert.pem",
			SMTPTLSKeyEnvironment:         "key.pem",
		},
		"implicit listen": {
			SMTPListenImplicitEnvironment: "2465",
			SMTPTLSCertEnvironment:        "cert.pem",
			SMTPTLSKeyEnvironment:         "key.pem",
		},
		"TLS certificate without key": {
			SMTPListenStartTLSEnvironment: "127.0.0.1:2587",
			SMTPTLSCertEnvironment:        "cert.pem",
		},
		"TLS key without certificate": {
			SMTPListenImplicitEnvironment: "127.0.0.1:2465",
			SMTPTLSKeyEnvironment:         "key.pem",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(newSources("", FileNotSelected), func(name string) (string, bool) {
				value, ok := environment[name]
				return value, ok
			})
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadRejectsDuplicateSMTPListenerAddresses(t *testing.T) {
	for name, contents := range map[string]string{
		"plain and STARTTLS": `
[smtp]
listen_plain = "127.0.0.1:2525"
listen_starttls = "127.0.0.1:2525"
[smtp.tls]
cert_file = "cert.pem"
key_file = "key.pem"
`,
		"STARTTLS and implicit": `
[smtp]
listen_plain = ""
listen_starttls = "127.0.0.1:2587"
listen_implicit = "127.0.0.1:2587"
[smtp.tls]
cert_file = "cert.pem"
key_file = "key.pem"
`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeFile(t, path, contents)
			_, err := load(newSources(path, FileSelectedByFlag), noEnvironment)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func noEnvironment(string) (string, bool) {
	return "", false
}
