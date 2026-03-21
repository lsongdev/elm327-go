package elm327

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
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
}

func (c *Client) Connect(addr string) (err error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", addr, err)
	}
	c.conn = conn
	c.r = bufio.NewReader(conn)
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return "", fmt.Errorf("not connected")
	}

	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("empty command")
	}

	writeTimeout := c.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = 5 * time.Second
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = c.conn.Write([]byte(strings.TrimSpace(command) + "\r"))
	if err != nil {
		return "", fmt.Errorf("failed to send command: %w", err)
	}

	readTimeout := c.ReadTimeout
	if readTimeout == 0 {
		readTimeout = 10 * time.Second
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(readTimeout))
	return c.readResponseLocked()
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

	// ELM327 prompt is '>' and lines are typically separated by \r (sometimes \n too).
	// We read until the prompt byte is observed, then normalize whitespace.
	var b strings.Builder
	for {
		ch, err := c.r.ReadByte()
		if err != nil {
			return "", fmt.Errorf("failed to read response: %w", err)
		}
		if ch == '>' {
			break
		}
		// Normalize control chars to spaces so strings.Fields can do the rest.
		if ch == '\r' || ch == '\n' || ch == '\t' {
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(ch)
	}

	// Collapse multiple spaces and trim.
	return strings.Join(strings.Fields(b.String()), " "), nil
}

// ReadPIDBytes reads a PID (e.g. "010C") and returns the data bytes from the response.
// For a typical response like "41 0C 1A F8", it returns []byte{0x1A, 0xF8}.
func (c *Client) ReadPIDBytes(pid string) ([]byte, error) {
	pid = strings.ToUpper(strings.TrimSpace(pid))
	// Use Call() to get the full raw line(s) (may include echo like "010C 41 0C ...").
	raw, err := c.Call(pid)
	if err != nil {
		return nil, err
	}
	return extractMode01DataBytes(pid, raw)
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

func extractMode01DataBytes(pid string, rawResp string) ([]byte, error) {
	resp := strings.TrimSpace(rawResp)
	if resp == "" {
		return []byte{}, nil
	}
	// Some adapters echo the request, so you may see "012F NO DATA".
	if strings.Contains(strings.ToUpper(resp), "NO DATA") {
		return nil, fmt.Errorf("no data for pid %s", pid)
	}

	// We expect a Mode 01 response: "41 <pidByte> <data...>"
	pidByte := ""
	if len(pid) >= 4 {
		pidByte = pid[2:4]
	}
	parts := strings.Fields(resp)
	for i := 0; i+2 < len(parts); i++ {
		if strings.EqualFold(parts[i], "41") {
			if pidByte == "" || strings.EqualFold(parts[i+1], pidByte) {
				// Parse the remaining tokens as 1-byte hex values, but ignore any non-byte tokens
				// (some adapters may include echoes or extra text).
				dataParts := parts[i+2:]
				out := make([]byte, 0, len(dataParts))
				for _, f := range dataParts {
					if len(f) != 2 {
						// stop at first non-byte token
						break
					}
					u, err := strconv.ParseUint(f, 16, 8)
					if err != nil {
						return nil, fmt.Errorf("invalid hex byte %q in %q: %w", f, rawResp, err)
					}
					out = append(out, byte(u))
				}
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("unexpected response for pid %s: %q", pid, rawResp)
}
