package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesDefaultsWithoutOtherSources(t *testing.T) {
	configuration, err := load(newSources("", FileNotSelected), noEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if configuration != Defaults() {
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
	if configuration != Defaults() {
		t.Fatalf("configuration = %+v, want %+v", configuration, Defaults())
	}
}

func TestLoadAppliesFileThenEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "[smtp]\nlisten = \"127.0.0.1:2500\"\nhostname = \"file.example\"\n")
	sources := newSources(path, FileSelectedByFlag)
	environment := map[string]string{
		SMTPListenEnvironment:   "127.0.0.1:2600",
		SMTPHostnameEnvironment: "env.example",
	}

	configuration, err := load(sources, func(name string) (string, bool) {
		value, ok := environment[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if configuration.SMTP.Listen != "127.0.0.1:2600" {
		t.Fatalf("smtp listen = %q", configuration.SMTP.Listen)
	}
	if configuration.SMTP.Hostname != "env.example" {
		t.Fatalf("smtp hostname = %q", configuration.SMTP.Hostname)
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
		"listen": {
			SMTPListenEnvironment: "2525",
		},
		"hostname": {
			SMTPHostnameEnvironment: "mail.example\r\ninjected",
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

func noEnvironment(string) (string, bool) {
	return "", false
}
