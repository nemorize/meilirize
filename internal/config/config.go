package config

import (
	"fmt"
	"net"
	"strings"
)

const (
	SMTPListenEnvironment   = EnvironmentPrefix + "SMTP_LISTEN"
	SMTPHostnameEnvironment = EnvironmentPrefix + "SMTP_HOSTNAME"
)

type Config struct {
	SMTP SMTPConfig `toml:"smtp"`
}

type SMTPConfig struct {
	Listen   string `toml:"listen"`
	Hostname string `toml:"hostname"`
}

func Defaults() Config {
	return Config{
		SMTP: SMTPConfig{
			Listen:   "127.0.0.1:2525",
			Hostname: "localhost",
		},
	}
}

func (configuration Config) Validate() error {
	if _, _, err := net.SplitHostPort(configuration.SMTP.Listen); err != nil {
		return fmt.Errorf("smtp.listen must be a host:port address: %w", err)
	}

	hostname := configuration.SMTP.Hostname
	if hostname == "" {
		return fmt.Errorf("smtp.hostname must not be empty")
	}
	if strings.ContainsAny(hostname, " \t\r\n") {
		return fmt.Errorf("smtp.hostname must not contain whitespace")
	}
	return nil
}
