package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
`)
	sources := newSources(path, FileSelectedByFlag)
	environment := map[string]string{
		SMTPListenPlainEnvironment:    "127.0.0.1:2600",
		SMTPListenStartTLSEnvironment: "127.0.0.1:2680",
		SMTPListenImplicitEnvironment: "127.0.0.1:2468",
		SMTPHostnameEnvironment:       "env.example",
		SMTPTLSCertEnvironment:        "env-cert.pem",
		SMTPTLSKeyEnvironment:         "env-key.pem",
		DatabasePathEnvironment:       "env.db",
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

func TestLoadRejectsInvalidSMTPSettings(t *testing.T) {
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
