package config

import (
	"bytes"
	"fmt"
	"os"

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
	}

	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}
	return configuration, nil
}
