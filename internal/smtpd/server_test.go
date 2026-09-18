package smtpd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSMTPConversation(t *testing.T) {
	server := newTestServer(t)
	listeners, cancel, done := startTestServer(t, server)
	client, err := net.DialTimeout("tcp", listeners[0].Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	connection := textproto.NewConn(client)
	expectResponse(t, connection, 220, "mail.example ESMTP Service Ready")

	if err := connection.PrintfLine("EHLO client.example"); err != nil {
		t.Fatal(err)
	}
	_, capabilities, err := connection.ReadResponse(250)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"PIPELINING", "8BITMIME", "ENHANCEDSTATUSCODES", "CHUNKING"} {
		if !strings.Contains(capabilities, expected) {
			t.Errorf("EHLO capabilities do not contain %q: %s", expected, capabilities)
		}
	}
	for _, unavailable := range []string{"AUTH", "STARTTLS"} {
		if strings.Contains(capabilities, unavailable) {
			t.Errorf("EHLO capabilities unexpectedly contain %q: %s", unavailable, capabilities)
		}
	}

	expectCommand(t, connection, "NOOP", 250, "2.0.0")
	expectCommand(t, connection, "RSET", 250, "2.0.0")
	expectCommand(t, connection, "MAIL FROM:<sender@example.com>", 502, "5.5.1 Mail transactions are not available")
	expectCommand(t, connection, "UNKNOWN", 501, "5.5.2 Bad command")
	expectCommand(t, connection, "QUIT", 221, "2.0.0 Bye")

	stopTestServer(t, cancel, done)
}

func TestServeStopsWhenContextIsCanceled(t *testing.T) {
	server := newTestServer(t)
	listeners, cancel, done := startTestServer(t, server)

	connection, err := net.DialTimeout("tcp", listeners[0].Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	client := textproto.NewConn(connection)
	expectResponse(t, client, 220, "mail.example ESMTP Service Ready")
	expectCommand(t, client, "QUIT", 221, "2.0.0 Bye")

	stopTestServer(t, cancel, done)
}

func TestServeStopsWhenContextIsAlreadyCanceled(t *testing.T) {
	server, _ := newTLSTestServer(t, ModeStartTLS, ModeImplicit)
	listeners, err := server.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listeners)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP server did not stop with an already canceled context")
	}
}

func TestSMTPSTARTTLSConversation(t *testing.T) {
	server, clientTLS := newTLSTestServer(t, ModeStartTLS)
	listeners, cancel, done := startTestServer(t, server)
	client, err := net.DialTimeout("tcp", listeners[0].Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	connection := textproto.NewConn(client)
	expectResponse(t, connection, 220, "mail.example ESMTP Service Ready")
	capabilities := ehlo(t, connection)
	if !strings.Contains(capabilities, "STARTTLS") {
		t.Fatalf("EHLO capabilities do not contain STARTTLS: %s", capabilities)
	}
	expectCommand(t, connection, "STARTTLS", 220, "Ready to start TLS")

	secureClient := tls.Client(client, clientTLS)
	if err := secureClient.HandshakeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if secureClient.ConnectionState().Version < tls.VersionTLS12 {
		t.Fatalf("negotiated TLS version = %x", secureClient.ConnectionState().Version)
	}
	secureConnection := textproto.NewConn(secureClient)
	capabilities = ehlo(t, secureConnection)
	if strings.Contains(capabilities, "STARTTLS") {
		t.Fatalf("EHLO capabilities still contain STARTTLS after upgrade: %s", capabilities)
	}
	expectCommand(t, secureConnection, "QUIT", 221, "2.0.0 Bye")

	stopTestServer(t, cancel, done)
}

func TestSMTPImplicitTLSConversation(t *testing.T) {
	server, clientTLS := newTLSTestServer(t, ModeImplicit)
	listeners, cancel, done := startTestServer(t, server)

	client, err := tls.Dial("tcp", listeners[0].Addr().String(), clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	connection := textproto.NewConn(client)
	expectResponse(t, connection, 220, "mail.example ESMTP Service Ready")
	capabilities := ehlo(t, connection)
	if strings.Contains(capabilities, "STARTTLS") {
		t.Fatalf("EHLO capabilities contain STARTTLS over implicit TLS: %s", capabilities)
	}
	expectCommand(t, connection, "QUIT", 221, "2.0.0 Bye")

	stopTestServer(t, cancel, done)
}

func TestSTARTTLSAndImplicitTLSListenersRunConcurrently(t *testing.T) {
	server, clientTLS := newTLSTestServer(t, ModeStartTLS, ModeImplicit)
	listeners, cancel, done := startTestServer(t, server)
	if len(listeners) != 2 {
		t.Fatalf("listener count = %d", len(listeners))
	}
	if listeners[0].Mode() != ModeStartTLS || listeners[1].Mode() != ModeImplicit {
		t.Fatalf("unexpected listener modes: %q, %q", listeners[0].Mode(), listeners[1].Mode())
	}

	startTLSClient, err := net.DialTimeout("tcp", listeners[0].Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := startTLSClient.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	startTLSConnection := textproto.NewConn(startTLSClient)
	expectResponse(t, startTLSConnection, 220, "mail.example ESMTP Service Ready")
	if capabilities := ehlo(t, startTLSConnection); !strings.Contains(capabilities, "STARTTLS") {
		t.Fatalf("EHLO capabilities do not contain STARTTLS: %s", capabilities)
	}
	expectCommand(t, startTLSConnection, "STARTTLS", 220, "Ready to start TLS")
	secureStartTLSClient := tls.Client(startTLSClient, clientTLS.Clone())
	if err := secureStartTLSClient.HandshakeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectCommand(t, textproto.NewConn(secureStartTLSClient), "QUIT", 221, "2.0.0 Bye")

	implicitClient, err := tls.Dial("tcp", listeners[1].Addr().String(), clientTLS.Clone())
	if err != nil {
		t.Fatal(err)
	}
	if err := implicitClient.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	implicitConnection := textproto.NewConn(implicitClient)
	expectResponse(t, implicitConnection, 220, "mail.example ESMTP Service Ready")
	expectCommand(t, implicitConnection, "QUIT", 221, "2.0.0 Bye")

	_ = secureStartTLSClient.Close()
	_ = implicitClient.Close()
	stopTestServer(t, cancel, done)
}

func TestNewRejectsUnreadableTLSCertificate(t *testing.T) {
	_, err := New(Config{
		Listeners:   []ListenerConfig{{Address: "127.0.0.1:0", Mode: ModeStartTLS}},
		Hostname:    "mail.example",
		TLSCertFile: filepath.Join(t.TempDir(), "missing-cert.pem"),
		TLSKeyFile:  filepath.Join(t.TempDir(), "missing-key.pem"),
	})
	if err == nil || !strings.Contains(err.Error(), "load SMTP TLS certificate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	server, err := New(Config{
		Listeners:    []ListenerConfig{{Address: "127.0.0.1:0", Mode: ModePlain}},
		Hostname:     "mail.example",
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func newTLSTestServer(t *testing.T, modes ...Mode) (*Server, *tls.Config) {
	t.Helper()
	certFile, keyFile, roots := writeTestCertificate(t)
	listeners := make([]ListenerConfig, 0, len(modes))
	for _, mode := range modes {
		listeners = append(listeners, ListenerConfig{Address: "127.0.0.1:0", Mode: mode})
	}
	server, err := New(Config{
		Listeners:    listeners,
		Hostname:     "mail.example",
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		TLSCertFile:  certFile,
		TLSKeyFile:   keyFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, &tls.Config{
		RootCAs:    roots,
		ServerName: "mail.example",
		MinVersion: tls.VersionTLS12,
	}
}

func startTestServer(t *testing.T, server *Server) ([]*Listener, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	listeners, err := server.Listen(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listeners)
	}()
	return listeners, cancel, done
}

func stopTestServer(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP server did not stop after cancellation")
	}
}

func writeTestCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "mail.example",
		},
		DNSNames:              []string{"mail.example"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	certFile := filepath.Join(directory, "cert.pem")
	keyFile := filepath.Join(directory, "key.pem")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	if err := os.WriteFile(certFile, certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, privateKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("append test certificate to root pool")
	}
	return certFile, keyFile, roots
}

func ehlo(t *testing.T, connection *textproto.Conn) string {
	t.Helper()
	if err := connection.PrintfLine("EHLO client.example"); err != nil {
		t.Fatal(err)
	}
	_, capabilities, err := connection.ReadResponse(250)
	if err != nil {
		t.Fatal(err)
	}
	return capabilities
}

func expectCommand(t *testing.T, connection *textproto.Conn, command string, code int, contains string) {
	t.Helper()
	if err := connection.PrintfLine("%s", command); err != nil {
		t.Fatal(err)
	}
	expectResponse(t, connection, code, contains)
}

func expectResponse(t *testing.T, connection *textproto.Conn, code int, contains string) {
	t.Helper()
	_, message, err := connection.ReadResponse(code)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, contains) {
		t.Fatalf("response = %q, want text containing %q", message, contains)
	}
}
