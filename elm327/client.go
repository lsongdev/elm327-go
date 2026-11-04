package elm327

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	defaultConnectTimeout = 10 * time.Second
	defaultReadTimeout    = 10 * time.Second
	defaultWriteTimeout   = 5 * time.Second
	defaultSyncTimeout    = 150 * time.Millisecond
	defaultSearchTimeout  = 30 * time.Second
	defaultMaxResponse    = 64 << 10
)

// ErrNotConnected is returned by commands on a closed or failed connection.
var ErrNotConnected = errors.New("not connected")

// Exchange is one command and its response, reported to Client.Trace.
type Exchange struct {
	Command  string
	Response string
	Duration time.Duration
	Err      error
}

// Client talks to an ELM327 adapter over TCP. Its methods are safe for
// concurrent use; commands on one connection run one at a time.
type Client struct {
	// mu serializes commands. connMu only guards the fields below it, so Close
	// can interrupt a command that is waiting for the adapter.
	mu     sync.Mutex
	connMu sync.Mutex
	conn   net.Conn
	r      *bufio.Reader

	// ReadTimeout applies to reading a full response (until '>' prompt).
	// Zero means 10 seconds.
	ReadTimeout time.Duration

	// WriteTimeout applies to writing a command (including trailing \r).
	// Zero means 5 seconds.
	WriteTimeout time.Duration

	// ConnectTimeout applies while establishing the TCP connection. Zero means
	// 10 seconds.
	ConnectTimeout time.Duration

	// SyncTimeout is the maximum time Connect waits for an optional prompt that
	// is already being sent by the adapter. Zero means 150 milliseconds.
	SyncTimeout time.Duration

	// SearchTimeout applies to the first vehicle query of a connection, while
	// the adapter may try every protocol. Zero means 30 seconds.
	SearchTimeout time.Duration

	// MaxResponseSize limits a response before the '>' prompt. Zero means 64 KiB.
	MaxResponseSize int

	// Trace, when set before connecting, receives every exchange. It runs on the
	// calling goroutine and must not call the Client.
	Trace func(Exchange)
}

// Connect establishes a TCP connection to a Wi-Fi ELM327 adapter.
func (c *Client) Connect(addr string) error {
	return c.ConnectContext(context.Background(), addr)
}

// ConnectContext establishes a TCP connection and consumes an optional prompt
// already waiting on the connection. It is safe to reconnect an existing Client.
func (c *Client) ConnectContext(ctx context.Context, addr string) error {
	timeout := c.ConnectTimeout
	if timeout == 0 {
		timeout = defaultConnectTimeout
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	_ = c.Close() // also ends a command still running on the old connection
	c.mu.Lock()
	defer c.mu.Unlock()
	r := bufio.NewReader(conn)
	if err := c.consumeInitialPrompt(conn, r); err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to synchronize with %s: %w", addr, err)
	}
	c.connMu.Lock()
	c.conn, c.r = conn, r
	c.connMu.Unlock()
	return nil
}

// Connected reports whether a connection is open. A command that fails on the
// wire closes the connection, so this turns false after a timeout.
func (c *Client) Connected() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.conn != nil
}

// Close closes the connection, interrupting any command in progress.
func (c *Client) Close() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn, c.r = nil, nil
	return err
}

// Call sends one command and returns the response lines, without the prompt.
func (c *Client) Call(command string) (resp string, err error) {
	return c.call(command, false, 0)
}

// Repeat sends only a carriage return, asking the ELM327 to execute its last
// command again. This is useful for high-frequency polling.
func (c *Client) Repeat() (resp string, err error) {
	return c.call("", true, 0)
}

// Initialize resets the adapter and selects predictable formatting with
// automatic protocol detection. Headers stay on so that responses can be told
// apart per ECU and multi-frame messages reassembled reliably. Call it after
// Connect unless preserving the adapter's existing configuration is intentional.
func (c *Client) Initialize() error {
	if _, err := c.Call("ATZ"); err != nil {
		return fmt.Errorf("reset adapter: %w", err)
	}
	for _, command := range []string{"ATE0", "ATL0", "ATS1", "ATH1", "ATCAF1", "ATSP0"} {
		response, err := c.Call(command)
		if err != nil {
			return fmt.Errorf("initialize adapter with %s: %w", command, err)
		}
		if !containsResponseLine(response, "OK") {
			return fmt.Errorf("initialize adapter with %s: unexpected response %q", command, response)
		}
	}
	return nil
}

func (c *Client) call(command string, allowEmpty bool, timeout time.Duration) (resp string, err error) {
	command = strings.TrimSpace(command)
	if command == "" && !allowEmpty {
		return "", fmt.Errorf("empty command")
	}
	if strings.ContainsAny(command, "\r\n\x00") {
		return "", fmt.Errorf("command contains a line terminator")
	}

	c.mu.Lock()
	start := time.Now()
	resp, err = c.exchange(command, allowEmpty, timeout)
	duration := time.Since(start)
	c.mu.Unlock()
	if c.Trace != nil {
		c.Trace(Exchange{Command: command, Response: resp, Duration: duration, Err: err})
	}
	return resp, err
}

// exchange runs one command. Callers hold c.mu. Any wire failure closes the
// connection, because a lost prompt would shift every later response.
func (c *Client) exchange(command string, allowEmpty bool, timeout time.Duration) (string, error) {
	c.connMu.Lock()
	conn, r := c.conn, c.r
	c.connMu.Unlock()
	if conn == nil {
		return "", ErrNotConnected
	}
	fail := func(err error) (string, error) {
		c.invalidate(conn)
		return "", err
	}

	writeTimeout := c.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = defaultWriteTimeout
	}
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fail(fmt.Errorf("failed to set write deadline: %w", err))
	}
	if err := writeFull(conn, []byte(command+"\r")); err != nil {
		return fail(fmt.Errorf("failed to send command: %w", err))
	}

	if timeout == 0 {
		timeout = c.ReadTimeout
	}
	if timeout == 0 {
		timeout = defaultReadTimeout
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return fail(fmt.Errorf("failed to set read deadline: %w", err))
	}

	// Ignore stray empty prompts. They are common when a TCP bridge connects to
	// an ELM327 that was already idle, and must not shift request/response pairs.
	for {
		resp, err := c.readResponse(r)
		if err != nil {
			return fail(err)
		}
		if resp != "" || allowEmpty {
			return resp, nil
		}
	}
}

func (c *Client) readResponse(r *bufio.Reader) (string, error) {
	// Preserve line boundaries: echo, status text, CAN headers and multiple ECU
	// replies must be parsed as separate lines.
	var b strings.Builder
	maxResponse := c.MaxResponseSize
	if maxResponse <= 0 {
		maxResponse = defaultMaxResponse
	}
	for {
		ch, err := r.ReadByte()
		if err != nil {
			return "", fmt.Errorf("failed to read response: %w", err)
		}
		if ch == '>' {
			break
		}
		if b.Len() >= maxResponse {
			return "", fmt.Errorf("response exceeds %d bytes", maxResponse)
		}
		b.WriteByte(ch)
	}
	return normalizeResponse(b.String()), nil
}

func (c *Client) consumeInitialPrompt(conn net.Conn, r *bufio.Reader) error {
	timeout := c.SyncTimeout
	if timeout == 0 {
		timeout = defaultSyncTimeout
	}
	if timeout < 0 {
		return nil
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	initial, err := r.ReadString('>')
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		// No initial prompt is valid for many transparent Wi-Fi bridges. If any
		// bytes were consumed, however, the stream is incomplete and unsafe.
		if initial == "" {
			return conn.SetReadDeadline(time.Time{})
		}
	}
	if err != nil {
		return err
	}
	return conn.SetReadDeadline(time.Time{})
}

// invalidate drops conn unless a reconnect has already replaced it.
func (c *Client) invalidate(conn net.Conn) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	_ = conn.Close()
	if c.conn == conn {
		c.conn, c.r = nil, nil
	}
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func containsResponseLine(response, want string) bool {
	for _, line := range strings.Split(response, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), want) {
			return true
		}
	}
	return false
}
