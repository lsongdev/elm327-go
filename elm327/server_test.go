package elm327

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientSynchronizesInitialPrompt(t *testing.T) {
	listener := listenLocal(t)
	server := &Server{
		NewHandler: func() HandlerFunc {
			return func(command string) (string, bool) {
				if normalizeCommand(command) == "010C" {
					return "010C\r41 0C 1A F8", true
				}
				return "?", true
			}
		},
		Logger: log.New(io.Discard, "", 0),
	}
	done := serveInBackground(server, listener)
	t.Cleanup(func() { stopServer(t, listener, done) })

	client := &Client{SyncTimeout: time.Second}
	if err := client.Connect(listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	rpm, err := client.EngineRPM()
	if err != nil {
		t.Fatal(err)
	}
	if rpm != 1726 {
		t.Fatalf("got RPM %v, want 1726", rpm)
	}
	repeated, err := client.Repeat()
	if err != nil || !strings.Contains(repeated, "41 0C 1A F8") {
		t.Fatalf("repeat response %q, error %v", repeated, err)
	}
}

func TestServerRepeatsLastCommand(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	count := 0
	server := &Server{Logger: log.New(io.Discard, "", 0)}
	go server.handleClient(serverConn, func(command string) (string, bool) {
		count++
		return fmt.Sprintf("%s\rVALUE %d", command, count), true
	})
	r := bufio.NewReader(clientConn)
	if got := readPrompt(t, r); got != "" {
		t.Fatalf("initial response = %q", got)
	}
	if _, err := io.WriteString(clientConn, "TEST\r"); err != nil {
		t.Fatal(err)
	}
	if got := readPrompt(t, r); got != "TEST\rVALUE 1\r" {
		t.Fatalf("first response = %q", got)
	}
	if _, err := io.WriteString(clientConn, "\r"); err != nil {
		t.Fatal(err)
	}
	if got := readPrompt(t, r); got != "VALUE 2\r" {
		t.Fatalf("repeated response = %q", got)
	}
}

func TestSessionFactoryIsolation(t *testing.T) {
	listener := listenLocal(t)
	server := &Server{
		NewHandler: func() HandlerFunc {
			count := 0
			return func(string) (string, bool) {
				count++
				return fmt.Sprintf("%d", count), true
			}
		},
		Logger: log.New(io.Discard, "", 0),
	}
	done := serveInBackground(server, listener)
	t.Cleanup(func() { stopServer(t, listener, done) })

	const clients = 12
	var wg sync.WaitGroup
	errCh := make(chan error, clients)
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &Client{SyncTimeout: time.Second}
			if err := client.Connect(listener.Addr().String()); err != nil {
				errCh <- err
				return
			}
			defer client.Close()
			for expected := 1; expected <= 20; expected++ {
				got, err := client.Call("X")
				if err != nil || got != fmt.Sprint(expected) {
					errCh <- fmt.Errorf("response %q error %v, want %d", got, err, expected)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestClientInvalidatesAfterTimeout(t *testing.T) {
	listener := listenLocal(t)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, ">")
		_, _ = bufio.NewReader(conn).ReadString('\r')
		time.Sleep(200 * time.Millisecond)
	}()
	client := &Client{ReadTimeout: 20 * time.Millisecond, SyncTimeout: time.Second}
	if err := client.Connect(listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call("010C"); err == nil {
		t.Fatal("expected timeout")
	}
	if _, err := client.Call("010C"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected invalidated connection, got %v", err)
	}
}

func TestReadCommandLimit(t *testing.T) {
	_, err := readCommand(bufio.NewReader(strings.NewReader("12345\r")), 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size error, got %v", err)
	}
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func serveInBackground(server *Server, listener net.Listener) <-chan error {
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	return done
}

func stopServer(t *testing.T, listener net.Listener, done <-chan error) {
	t.Helper()
	_ = listener.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("server stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("server did not stop")
	}
}

func readPrompt(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	response, err := r.ReadString('>')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(response, ">")
}
