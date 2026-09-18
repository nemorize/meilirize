package config

import (
	"fmt"
	"net"
	"strings"
)

const (
	SMTPListenPlainEnvironment    = EnvironmentPrefix + "SMTP_LISTEN_PLAIN"
	SMTPListenStartTLSEnvironment = EnvironmentPrefix + "SMTP_LISTEN_STARTTLS"
	SMTPListenImplicitEnvironment = EnvironmentPrefix + "SMTP_LISTEN_IMPLICIT"
	SMTPHostnameEnvironment       = EnvironmentPrefix + "SMTP_HOSTNAME"
	SMTPTLSCertEnvironment        = EnvironmentPrefix + "SMTP_TLS_CERT_FILE"
	SMTPTLSKeyEnvironment         = EnvironmentPrefix + "SMTP_TLS_KEY_FILE"

	SMTPModePlain    = "plain"
	SMTPModeStartTLS = "starttls"
	SMTPModeImplicit = "implicit"
)

type Config struct {
	SMTP SMTPConfig `toml:"smtp"`
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

func Defaults() Config {
	return Config{
		SMTP: SMTPConfig{
			ListenPlain: "127.0.0.1:2525",
			Hostname:    "localhost",
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
