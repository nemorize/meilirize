package smtpd

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	smtp "github.com/emersion/go-smtp"
)

type Mode string

const (
	ModePlain    Mode = "plain"
	ModeStartTLS Mode = "starttls"
	ModeImplicit Mode = "implicit"
)

var errMailTransactionsUnavailable = &smtp.SMTPError{
	Code:         502,
	EnhancedCode: smtp.EnhancedCode{5, 5, 1},
	Message:      "Mail transactions are not available",
}

type Config struct {
	Listeners    []ListenerConfig
	Hostname     string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	TLSCertFile  string
	TLSKeyFile   string
}

type ListenerConfig struct {
	Address string
	Mode    Mode
}

type endpoint struct {
	address  string
	mode     Mode
	protocol *smtp.Server
}

type Server struct {
	endpoints []endpoint
}

type Listener struct {
	net.Listener
	protocol *smtp.Server
	mode     Mode
}

func (listener *Listener) Mode() Mode {
	return listener.mode
}

func New(configuration Config) (*Server, error) {
	if configuration.Hostname == "" || strings.ContainsAny(configuration.Hostname, " \t\r\n") {
		return nil, fmt.Errorf("invalid SMTP hostname %q", configuration.Hostname)
	}
	if len(configuration.Listeners) == 0 {
		return nil, fmt.Errorf("at least one SMTP listener is required")
	}
	if configuration.ReadTimeout <= 0 {
		configuration.ReadTimeout = 5 * time.Minute
	}
	if configuration.WriteTimeout <= 0 {
		configuration.WriteTimeout = 30 * time.Second
	}

	listeners := append([]ListenerConfig(nil), configuration.Listeners...)
	tlsEnabled := false
	seen := make(map[string]struct{}, len(listeners))
	for index := range listeners {
		listener := &listeners[index]
		_, port, err := net.SplitHostPort(listener.Address)
		if err != nil {
			return nil, fmt.Errorf("invalid SMTP listen address %q: %w", listener.Address, err)
		}
		if _, ok := seen[listener.Address]; ok && port != "0" {
			return nil, fmt.Errorf("duplicate SMTP listen address %q", listener.Address)
		}
		seen[listener.Address] = struct{}{}
		if listener.Mode == "" {
			listener.Mode = ModePlain
		}
		switch listener.Mode {
		case ModePlain:
		case ModeStartTLS, ModeImplicit:
			tlsEnabled = true
		default:
			return nil, fmt.Errorf("invalid SMTP listener mode %q", listener.Mode)
		}
	}

	var tlsConfiguration *tls.Config
	if tlsEnabled {
		if configuration.TLSCertFile == "" {
			return nil, fmt.Errorf("SMTP TLS certificate file must not be empty")
		}
		if configuration.TLSKeyFile == "" {
			return nil, fmt.Errorf("SMTP TLS key file must not be empty")
		}
		certificate, err := tls.LoadX509KeyPair(configuration.TLSCertFile, configuration.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load SMTP TLS certificate: %w", err)
		}
		tlsConfiguration = &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		}
	}

	endpoints := make([]endpoint, 0, len(listeners))
	for _, listener := range listeners {
		protocol := smtp.NewServer(rejectingBackend{})
		protocol.Domain = configuration.Hostname
		protocol.ReadTimeout = configuration.ReadTimeout
		protocol.WriteTimeout = configuration.WriteTimeout
		protocol.AllowInsecureAuth = false
		if listener.Mode != ModePlain {
			protocol.TLSConfig = tlsConfiguration
		}
		endpoints = append(endpoints, endpoint{
			address:  listener.Address,
			mode:     listener.Mode,
			protocol: protocol,
		})
	}

	return &Server{endpoints: endpoints}, nil
}

func (server *Server) Listen(ctx context.Context) ([]*Listener, error) {
	listeners := make([]*Listener, 0, len(server.endpoints))
	for _, endpoint := range server.endpoints {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", endpoint.address)
		if err != nil {
			closeListeners(listeners)
			return nil, fmt.Errorf("listen for SMTP on %s: %w", endpoint.address, err)
		}
		if endpoint.mode == ModeImplicit {
			listener = tls.NewListener(listener, endpoint.protocol.TLSConfig)
		}
		listeners = append(listeners, &Listener{
			Listener: listener,
			protocol: endpoint.protocol,
			mode:     endpoint.mode,
		})
	}
	return listeners, nil
}

func (server *Server) Serve(ctx context.Context, listeners []*Listener) error {
	if len(listeners) != len(server.endpoints) {
		return fmt.Errorf("SMTP listener count does not match configured endpoints")
	}

	serveContext, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		address string
		err     error
	}
	results := make(chan result, len(listeners))
	for _, listener := range listeners {
		go func(listener *Listener) {
			results <- result{
				address: listener.Addr().String(),
				err:     serveProtocol(serveContext, listener.protocol, listener.Listener),
			}
		}(listener)
	}

	first := <-results
	cancel()
	for range len(listeners) - 1 {
		<-results
	}
	if ctx.Err() != nil {
		return nil
	}
	if first.err != nil {
		return fmt.Errorf("serve SMTP on %s: %w", first.address, first.err)
	}
	return fmt.Errorf("SMTP listener on %s stopped unexpectedly", first.address)
}

func serveProtocol(ctx context.Context, protocol *smtp.Server, listener net.Listener) error {
	result := make(chan error, 1)
	go func() {
		result <- protocol.Serve(listener)
	}()

	select {
	case err := <-result:
		_ = protocol.Close()
		if ctx.Err() != nil {
			return nil
		}
		return err
	case <-ctx.Done():
		_ = listener.Close()
		<-result
		_ = protocol.Close()
		return nil
	}
}

func closeListeners(listeners []*Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}

type rejectingBackend struct{}

func (rejectingBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return rejectingSession{}, nil
}

type rejectingSession struct{}

func (rejectingSession) Reset() {}

func (rejectingSession) Logout() error {
	return nil
}

func (rejectingSession) Mail(string, *smtp.MailOptions) error {
	return errMailTransactionsUnavailable
}

func (rejectingSession) Rcpt(string, *smtp.RcptOptions) error {
	return errMailTransactionsUnavailable
}

func (rejectingSession) Data(message io.Reader) error {
	_, _ = io.Copy(io.Discard, message)
	return errMailTransactionsUnavailable
}
