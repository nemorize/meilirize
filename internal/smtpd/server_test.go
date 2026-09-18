package smtpd

import (
	"context"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestSMTPConversation(t *testing.T) {
	server := newTestServer(t)
	client, serverConnection := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	listener := newSingleConnectionListener(serverConnection)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listener)
	}()

	connection := textproto.NewConn(client)
	defer connection.Close()
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

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeStopsWhenContextIsCanceled(t *testing.T) {
	server := newTestServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, listener)
	}()

	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
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

func newTestServer(t *testing.T) *Server {
	t.Helper()
	server, err := New(Config{
		ListenAddress: "127.0.0.1:0",
		Hostname:      "mail.example",
		ReadTimeout:   time.Second,
		WriteTimeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
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

type singleConnectionListener struct {
	connection net.Conn
	closed     chan struct{}
}

func newSingleConnectionListener(connection net.Conn) *singleConnectionListener {
	return &singleConnectionListener{connection: connection, closed: make(chan struct{})}
}

func (listener *singleConnectionListener) Accept() (net.Conn, error) {
	if listener.connection != nil {
		connection := listener.connection
		listener.connection = nil
		return connection, nil
	}
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *singleConnectionListener) Close() error {
	select {
	case <-listener.closed:
	default:
		close(listener.closed)
	}
	return nil
}

func (listener *singleConnectionListener) Addr() net.Addr {
	return testAddress("pipe")
}

type testAddress string

func (address testAddress) Network() string { return "pipe" }
func (address testAddress) String() string  { return string(address) }
