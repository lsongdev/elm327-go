package elm327

import (
	"bufio"
	"context"
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
	defaultMaxResponse    = 64 << 10
)

type Client struct {
	conn net.Conn
	r    *bufio.Reader
	mu   sync.Mutex

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

	// MaxResponseSize limits a response before the '>' prompt. Zero means 64 KiB.
	MaxResponseSize int
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

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn = conn
	c.r = bufio.NewReader(conn)
	if err := c.consumeInitialPromptLocked(); err != nil {
		c.invalidateLocked()
		return fmt.Errorf("failed to synchronize with %s: %w", addr, err)
	}
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		c.r = nil
		return err
	}
	return nil
}

func (c *Client) Call(command string) (resp string, err error) {
	return c.call(command, false)
}

// Repeat sends only a carriage return, asking the ELM327 to execute its last
// command again. This is useful for high-frequency polling.
func (c *Client) Repeat() (resp string, err error) {
	return c.call("", true)
}

// Initialize resets the adapter and selects predictable formatting with
// automatic protocol detection. Call it after Connect unless preserving the
// adapter's existing configuration is intentional.
func (c *Client) Initialize() error {
	if _, err := c.Call("ATZ"); err != nil {
		return fmt.Errorf("reset adapter: %w", err)
	}
	for _, command := range []string{"ATE0", "ATL0", "ATS1", "ATH0", "ATCAF1", "ATSP0"} {
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

func (c *Client) call(command string, allowEmpty bool) (resp string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return "", fmt.Errorf("not connected")
	}

	command = strings.TrimSpace(command)
	if command == "" && !allowEmpty {
		return "", fmt.Errorf("empty command")
	}
	if strings.ContainsAny(command, "\r\n\x00") {
		return "", fmt.Errorf("command contains a line terminator")
	}

	writeTimeout := c.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = defaultWriteTimeout
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		c.invalidateLocked()
		return "", fmt.Errorf("failed to set write deadline: %w", err)
	}
	if err := writeFull(c.conn, []byte(command+"\r")); err != nil {
		c.invalidateLocked()
		return "", fmt.Errorf("failed to send command: %w", err)
	}

	readTimeout := c.ReadTimeout
	if readTimeout == 0 {
		readTimeout = defaultReadTimeout
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		c.invalidateLocked()
		return "", fmt.Errorf("failed to set read deadline: %w", err)
	}

	// Ignore stray empty prompts. They are common when a TCP bridge connects to
	// an ELM327 that was already idle, and must not shift request/response pairs.
	for {
		resp, err = c.readResponseLocked()
		if err != nil {
			c.invalidateLocked()
			return "", err
		}
		if resp != "" || allowEmpty {
			return resp, nil
		}
	}
}

// func (c *Client) ReadPID(pid string) (value string, err error) {
// 	pid = strings.ToUpper(strings.TrimSpace(pid))
// 	response, err := c.Call(pid)
// 	if err != nil {
// 		return "", err
// 	}

// 	resp := strings.TrimSpace(response)
// 	if resp == "" {
// 		return "", nil
// 	}
// 	if strings.EqualFold(resp, "NO DATA") {
// 		return "", fmt.Errorf("no data for pid %s", pid)
// 	}

// 	// Typical response: "41 10 00 00 00 00"
// 	parts := strings.Fields(resp)
// 	if len(parts) >= 3 && strings.EqualFold(parts[0], "41") {
// 		// parts[1] is PID byte (e.g. "10") - verify it matches request when possible.
// 		reqPIDByte := ""
// 		if len(pid) >= 4 {
// 			reqPIDByte = pid[2:4]
// 		}
// 		if reqPIDByte == "" || strings.EqualFold(parts[1], reqPIDByte) {
// 			return strings.Join(parts[2:], " "), nil
// 		}
// 	}
// 	// Fallback: return raw response as-is.
// 	return resp, nil
// }

func (c *Client) readResponseLocked() (string, error) {
	if c.conn == nil || c.r == nil {
		return "", fmt.Errorf("not connected")
	}

	// Preserve line boundaries: echo, status text, CAN headers and multiple ECU
	// replies must be parsed as separate lines.
	var b strings.Builder
	maxResponse := c.MaxResponseSize
	if maxResponse <= 0 {
		maxResponse = defaultMaxResponse
	}
	for {
		ch, err := c.r.ReadByte()
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

// ReadPIDBytes reads a PID (e.g. "010C") and returns the data bytes from the response.
// For a typical response like "41 0C 1A F8", it returns []byte{0x1A, 0xF8}.
func (c *Client) ReadPIDBytes(pid string) ([]byte, error) {
	responses, err := c.ReadPIDResponses(pid)
	if err != nil {
		return nil, err
	}
	return responses[0], nil
}

// ReadPIDResponses returns one decoded data payload per responding ECU. It
// accepts normal, header-enabled and space-disabled ELM327 output.
func (c *Client) ReadPIDResponses(pid string) ([][]byte, error) {
	pid, err := normalizeMode01PID(pid)
	if err != nil {
		return nil, err
	}
	raw, err := c.Call(pid)
	if err != nil {
		return nil, err
	}
	return extractMode01Responses(pid, raw)
}

// EngineRPM returns engine speed in RPM (PID 010C).
// Formula: RPM = ((A*256)+B)/4
func (c *Client) EngineRPM() (float64, error) {
	b, err := c.ReadPIDBytes(PIDEngineRPM)
	if err != nil {
		return 0, err
	}
	if len(b) < 2 {
		return 0, fmt.Errorf("pid %s expected 2 bytes, got %d", PIDEngineRPM, len(b))
	}
	v := (int(b[0])*256 + int(b[1]))
	return float64(v) / 4.0, nil
}

// VehicleSpeed returns vehicle speed in km/h (PID 010D).
// Formula: km/h = A
func (c *Client) VehicleSpeed() (int, error) {
	b, err := c.ReadPIDBytes(PIDVehicleSpeed)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDVehicleSpeed, len(b))
	}
	return int(b[0]), nil
}

// EngineCoolantTemp returns coolant temperature in °C (PID 0105).
// Formula: °C = A - 40
func (c *Client) EngineCoolantTemp() (int, error) {
	b, err := c.ReadPIDBytes(PIDEngineCoolantTemp)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDEngineCoolantTemp, len(b))
	}
	return int(b[0]) - 40, nil
}

// IntakeAirTemp returns intake air temperature in °C (PID 010F).
// Formula: °C = A - 40
func (c *Client) IntakeAirTemp() (int, error) {
	b, err := c.ReadPIDBytes(PIDIntakeAirTemp)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDIntakeAirTemp, len(b))
	}
	return int(b[0]) - 40, nil
}

// EngineLoad returns calculated engine load in percent (PID 0104).
// Formula: % = (A * 100) / 255
func (c *Client) EngineLoad() (float64, error) {
	b, err := c.ReadPIDBytes(PIDEngineLoad)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDEngineLoad, len(b))
	}
	return float64(b[0]) * 100.0 / 255.0, nil
}

// ThrottlePosition returns throttle position in percent (PID 0111).
// Formula: % = (A * 100) / 255
func (c *Client) ThrottlePosition() (float64, error) {
	b, err := c.ReadPIDBytes(PIDThrottlePosition)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDThrottlePosition, len(b))
	}
	return float64(b[0]) * 100.0 / 255.0, nil
}

// IntakeMAP returns intake manifold absolute pressure in kPa (PID 010B).
// Formula: kPa = A
func (c *Client) IntakeMAP() (int, error) {
	b, err := c.ReadPIDBytes(PIDIntakeMAP)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDIntakeMAP, len(b))
	}
	return int(b[0]), nil
}

// MAF returns mass air flow in g/s (PID 0110).
// Formula: g/s = ((A*256)+B) / 100
func (c *Client) MAF() (float64, error) {
	b, err := c.ReadPIDBytes(PIDMAF)
	if err != nil {
		return 0, err
	}
	if len(b) < 2 {
		return 0, fmt.Errorf("pid %s expected 2 bytes, got %d", PIDMAF, len(b))
	}
	v := (int(b[0])*256 + int(b[1]))
	return float64(v) / 100.0, nil
}

// TimingAdvance returns timing advance in degrees (PID 010E).
// Formula: ° = (A / 2) - 64
func (c *Client) TimingAdvance() (float64, error) {
	b, err := c.ReadPIDBytes(PIDTimingAdvance)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDTimingAdvance, len(b))
	}
	return float64(b[0])/2.0 - 64.0, nil
}

// FuelLevel returns fuel tank level input in percent (PID 012F).
// Formula: % = (A * 100) / 255
func (c *Client) FuelLevel() (float64, error) {
	b, err := c.ReadPIDBytes(PIDFuelLevel)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDFuelLevel, len(b))
	}
	return float64(b[0]) * 100.0 / 255.0, nil
}

// ControlModuleVoltage returns control module voltage in volts (PID 0142).
// Formula: V = ((A*256)+B) / 1000
func (c *Client) ControlModuleVoltage() (float64, error) {
	b, err := c.ReadPIDBytes(PIDControlModuleVoltage)
	if err != nil {
		return 0, err
	}
	if len(b) < 2 {
		return 0, fmt.Errorf("pid %s expected 2 bytes, got %d", PIDControlModuleVoltage, len(b))
	}
	v := (int(b[0])*256 + int(b[1]))
	return float64(v) / 1000.0, nil
}

// RuntimeSinceEngineStart returns engine run time in seconds (PID 011F).
// Formula: seconds = (A*256)+B
func (c *Client) RuntimeSinceEngineStart() (int, error) {
	b, err := c.ReadPIDBytes(PIDRuntimeSinceEngineStart)
	if err != nil {
		return 0, err
	}
	if len(b) < 2 {
		return 0, fmt.Errorf("pid %s expected 2 bytes, got %d", PIDRuntimeSinceEngineStart, len(b))
	}
	return int(b[0])*256 + int(b[1]), nil
}

// BarometricPressure returns barometric pressure in kPa (PID 0133).
// Formula: kPa = A
func (c *Client) BarometricPressure() (int, error) {
	b, err := c.ReadPIDBytes(PIDBarometricPressure)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDBarometricPressure, len(b))
	}
	return int(b[0]), nil
}

// AmbientAirTemp returns ambient air temperature in °C (PID 0146).
// Formula: °C = A - 40
func (c *Client) AmbientAirTemp() (int, error) {
	b, err := c.ReadPIDBytes(PIDAmbientAirTemp)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 {
		return 0, fmt.Errorf("pid %s expected 1 byte, got %d", PIDAmbientAirTemp, len(b))
	}
	return int(b[0]) - 40, nil
}

func (c *Client) consumeInitialPromptLocked() error {
	timeout := c.SyncTimeout
	if timeout == 0 {
		timeout = defaultSyncTimeout
	}
	if timeout < 0 {
		return nil
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	initial, err := c.r.ReadString('>')
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		// No initial prompt is valid for many transparent Wi-Fi bridges. If any
		// bytes were consumed, however, the stream is incomplete and unsafe.
		if initial == "" {
			return c.conn.SetReadDeadline(time.Time{})
		}
	}
	if err != nil {
		return err
	}
	return c.conn.SetReadDeadline(time.Time{})
}

func (c *Client) invalidateLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn = nil
	c.r = nil
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
