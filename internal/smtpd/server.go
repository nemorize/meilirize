package smtpd

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	smtp "github.com/emersion/go-smtp"
)

const shutdownTimeout = 5 * time.Second

var errMailTransactionsUnavailable = &smtp.SMTPError{
	Code:         502,
	EnhancedCode: smtp.EnhancedCode{5, 5, 1},
	Message:      "Mail transactions are not available",
}

type Config struct {
	ListenAddress string
	Hostname      string
	ReadTimeout   time.Duration
	WriteTimeout  time.Duration
}

type Server struct {
	listenAddress string
	protocol      *smtp.Server
}

func New(configuration Config) (*Server, error) {
	if _, _, err := net.SplitHostPort(configuration.ListenAddress); err != nil {
		return nil, fmt.Errorf("invalid SMTP listen address: %w", err)
	}
	if configuration.Hostname == "" || strings.ContainsAny(configuration.Hostname, " \t\r\n") {
		return nil, fmt.Errorf("invalid SMTP hostname %q", configuration.Hostname)
	}
	if configuration.ReadTimeout <= 0 {
		configuration.ReadTimeout = 5 * time.Minute
	}
	if configuration.WriteTimeout <= 0 {
		configuration.WriteTimeout = 30 * time.Second
	}

	protocol := smtp.NewServer(rejectingBackend{})
	protocol.Domain = configuration.Hostname
	protocol.ReadTimeout = configuration.ReadTimeout
	protocol.WriteTimeout = configuration.WriteTimeout
	protocol.AllowInsecureAuth = false

	return &Server{
		listenAddress: configuration.ListenAddress,
		protocol:      protocol,
	}, nil
}

func (server *Server) Listen(ctx context.Context) (net.Listener, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", server.listenAddress)
	if err != nil {
		return nil, fmt.Errorf("listen for SMTP on %s: %w", server.listenAddress, err)
	}
	return listener, nil
}

func (server *Server) Serve(ctx context.Context, listener net.Listener) error {
	stopShutdown := make(chan struct{})
	shutdownComplete := make(chan struct{})
	go func() {
		defer close(shutdownComplete)
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := server.protocol.Shutdown(shutdownContext); err != nil {
				_ = server.protocol.Close()
			}
		case <-stopShutdown:
		}
	}()

	err := server.protocol.Serve(listener)
	close(stopShutdown)
	<-shutdownComplete
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("serve SMTP: %w", err)
	}
	return nil
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
