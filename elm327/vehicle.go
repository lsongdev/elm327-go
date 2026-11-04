package elm327

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lsongdev/elm327-go/obd"
)

// ECUReading is one ECU's answer to a PID.
type ECUReading struct {
	ECU string `json:"ecu,omitempty"`
	obd.Reading
}

// FreezeFrame is the snapshot an ECU stored when a code was set (Mode 02).
type FreezeFrame struct {
	DTC      obd.DTC       `json:"dtc"`
	Readings []obd.Reading `json:"readings"`
}

// AdapterInfo identifies the adapter and the protocol it selected. Clones often
// answer only some of these; unanswered ones stay empty.
type AdapterInfo struct {
	Version        string   `json:"version,omitempty"`    // ATI
	Identifier     string   `json:"identifier,omitempty"` // AT@1
	Protocol       string   `json:"protocol,omitempty"`   // ATDP
	ProtocolNumber string   `json:"protocol_number,omitempty"`
	Voltage        *float64 `json:"voltage,omitempty"` // ATRV, at the OBD connector
}

// IsNotAvailable reports errors that mean the vehicle does not offer something,
// as opposed to the adapter or connection failing.
func IsNotAvailable(err error) bool {
	if errors.Is(err, ErrNoData) {
		return true
	}
	var refused *NegativeResponseError
	return errors.As(err, &refused)
}

// Query sends an OBD request such as "010C" and returns the answering ECUs'
// messages, sorted by address.
func (c *Client) Query(request string) ([]Message, error) {
	return c.query(request, 0)
}

func (c *Client) query(request string, timeout time.Duration) ([]Message, error) {
	normalized, req, err := parseRequest(request)
	if err != nil {
		return nil, err
	}
	raw, err := c.call(normalized, false, timeout)
	if err != nil {
		return nil, err
	}
	messages, err := ParseMessages(normalized, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", normalized, err)
	}
	matched := matchResponse(req, messages)
	if len(matched) == 0 {
		return nil, fmt.Errorf("%s: unexpected response: %q", normalized, raw)
	}
	return matched, nil
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

// ReadPID reads and decodes a Mode 01 PID. When several ECUs answer, the lowest
// address wins, which on CAN is the engine controller.
func (c *Client) ReadPID(pid byte) (obd.Reading, error) {
	all, err := c.ReadPIDAll(pid)
	if err != nil {
		return obd.Reading{}, err
	}
	return all[0].Reading, nil
}

// ReadPIDAll reads and decodes a Mode 01 PID from every ECU that answers.
func (c *Client) ReadPIDAll(pid byte) ([]ECUReading, error) {
	messages, err := c.Query(fmt.Sprintf("01%02X", pid))
	if err != nil {
		return nil, err
	}
	return decodeReadings(pid, messages, 2)
}

// decodeReadings decodes each message's data, which starts at offset.
func decodeReadings(pid byte, messages []Message, offset int) ([]ECUReading, error) {
	var out []ECUReading
	var firstErr error
	for _, m := range messages {
		if len(m.Data) < offset {
			continue
		}
		reading, err := obd.Decode(pid, m.Data[offset:])
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, ECUReading{ECU: m.ECU, Reading: reading})
	}
	if len(out) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("pid %02X: empty response", pid)
		}
		return nil, firstErr
	}
	return out, nil
}

// SupportedPIDs walks the "supported PIDs" bitmaps of all ECUs and returns the
// PIDs in order, including the range PIDs themselves. Its first query may start
// the adapter's protocol search, so it allows SearchTimeout. A vehicle that is
// off answers with ErrNoData or an *AdapterError ("UNABLE TO CONNECT").
func (c *Client) SupportedPIDs() ([]byte, error) {
	timeout := c.SearchTimeout
	if timeout == 0 {
		timeout = defaultSearchTimeout
	}
	var pids []byte
	for base := 0; base <= 0xE0; base += 0x20 {
		messages, err := c.query(fmt.Sprintf("01%02X", base), timeout)
		timeout = 0
		if err != nil {
			if base > 0 && IsNotAvailable(err) {
				break // a range the ECU advertised but does not answer
			}
			return nil, err
		}
		for _, m := range messages {
			if len(m.Data) < 2 {
				continue
			}
			found, err := obd.ParseSupported(byte(base), m.Data[2:])
			if err != nil {
				continue
			}
			pids = append(pids, found...)
		}
		if !slices.Contains(pids, byte(base+0x20)) {
			break
		}
	}
	slices.Sort(pids)
	return slices.Compact(pids), nil
}

// MonitorStatus reads PID 01: the MIL, the DTC count and readiness monitors.
func (c *Client) MonitorStatus() (obd.MonitorStatus, error) {
	messages, err := c.Query("0101")
	if err != nil {
		return obd.MonitorStatus{}, err
	}
	var merged obd.MonitorStatus
	for i, m := range messages {
		if len(m.Data) < 2 {
			continue
		}
		status, err := obd.ParseMonitorStatus(m.Data[2:])
		if err != nil {
			if i == 0 {
				return obd.MonitorStatus{}, err
			}
			continue
		}
		if i == 0 {
			merged = status
			continue
		}
		merged.MIL = merged.MIL || status.MIL
		merged.DTCCount += status.DTCCount
	}
	return merged, nil
}

// ReadDTCs reads trouble codes of one kind from every ECU. A vehicle with none
// returns an empty list, including ECUs that answer NO DATA to Mode 07 or 0A.
func (c *Client) ReadDTCs(kind obd.DTCKind) ([]obd.DTC, error) {
	messages, err := c.Query(fmt.Sprintf("%02X", byte(kind)))
	if err != nil {
		if IsNotAvailable(err) {
			return []obd.DTC{}, nil
		}
		return nil, err
	}
	out := []obd.DTC{}
	for _, m := range messages {
		for _, code := range obd.ParseDTCs(m.Data) {
			dtc := obd.NewDTC(code)
			dtc.ECU = m.ECU
			out = append(out, dtc)
		}
	}
	return out, nil
}

// ClearDTCs sends Mode 04. Besides the codes this erases freeze frames, oxygen
// sensor test results and readiness monitors, so the car needs a full drive
// cycle before it can pass an emissions check. ECUs commonly refuse with the
// engine running.
func (c *Client) ClearDTCs() error {
	_, err := c.Query("04")
	return err
}

// FreezeFrame reads the Mode 02 snapshot of frame 0. It returns nil when no
// frame is stored.
func (c *Client) FreezeFrame() (*FreezeFrame, error) {
	messages, err := c.Query("020200")
	if err != nil {
		if IsNotAvailable(err) {
			return nil, nil
		}
		return nil, err
	}
	data := messages[0].Data
	if len(data) < 5 || (data[3] == 0 && data[4] == 0) {
		return nil, nil
	}
	frame := &FreezeFrame{DTC: obd.NewDTC(obd.DecodeDTC(data[3], data[4])), Readings: []obd.Reading{}}
	frame.DTC.ECU = messages[0].ECU

	for base := 0; base <= 0xE0; base += 0x20 {
		messages, err := c.Query(fmt.Sprintf("02%02X00", base))
		if err != nil {
			if IsNotAvailable(err) {
				break
			}
			return nil, err
		}
		if len(messages[0].Data) < 3 {
			break
		}
		pids, err := obd.ParseSupported(byte(base), messages[0].Data[3:])
		if err != nil {
			break
		}
		for _, pid := range pids {
			if obd.IsBitmap(pid) || pid == 0x02 {
				continue
			}
			values, err := c.Query(fmt.Sprintf("02%02X00", pid))
			if err != nil {
				if IsNotAvailable(err) {
					continue
				}
				return nil, err
			}
			readings, err := decodeReadings(pid, values, 3)
			if err != nil {
				continue
			}
			frame.Readings = append(frame.Readings, readings[0].Reading)
		}
		if !slices.Contains(pids, byte(base+0x20)) {
			break
		}
	}
	return frame, nil
}

// VehicleInfo reads the Mode 09 identification. Items the vehicle does not
// provide stay empty; older cars may provide none.
func (c *Client) VehicleInfo() (obd.VehicleInfo, error) {
	var info obd.VehicleInfo
	read := func(infoType byte, use func(ecu string, data []byte)) error {
		messages, err := c.Query(fmt.Sprintf("09%02X", infoType))
		if err != nil {
			if IsNotAvailable(err) {
				return nil
			}
			return err
		}
		for _, m := range messages {
			if m.ECU != "" && !slices.Contains(info.ECUs, m.ECU) {
				info.ECUs = append(info.ECUs, m.ECU)
			}
			if len(m.Data) > 2 {
				use(m.ECU, m.Data[2:])
			}
		}
		return nil
	}
	steps := []struct {
		infoType byte
		use      func(string, []byte)
	}{
		{obd.InfoVIN, func(_ string, data []byte) {
			if info.VIN == "" {
				info.VIN = obd.ParseVIN(data)
			}
		}},
		{obd.InfoCalibrationID, func(_ string, data []byte) {
			info.CalibrationIDs = append(info.CalibrationIDs, obd.ParseCalibrationIDs(data)...)
		}},
		{obd.InfoCVN, func(_ string, data []byte) {
			info.CVNs = append(info.CVNs, obd.ParseCVNs(data)...)
		}},
		{obd.InfoECUName, func(_ string, data []byte) {
			if info.ECUName == "" {
				info.ECUName = obd.ParseECUName(data)
			}
		}},
	}
	for _, step := range steps {
		if err := read(step.infoType, step.use); err != nil {
			return info, err
		}
	}
	slices.Sort(info.ECUs)
	return info, nil
}

// AdapterInfo reads what the adapter says about itself. Only a connection
// failure is an error; an adapter that answers "?" just leaves a field empty.
func (c *Client) AdapterInfo() (AdapterInfo, error) {
	var info AdapterInfo
	steps := []struct {
		command string
		use     func(string)
	}{
		{"ATI", func(s string) { info.Version = s }},
		{"AT@1", func(s string) { info.Identifier = s }},
		{"ATDP", func(s string) { info.Protocol = s }},
		{"ATDPN", func(s string) { info.ProtocolNumber = s }},
		{"ATRV", func(s string) {
			if v, err := strconv.ParseFloat(strings.TrimSuffix(strings.ToUpper(s), "V"), 64); err == nil {
				info.Voltage = &v
			}
		}},
	}
	for _, step := range steps {
		response, err := c.Call(step.command)
		if err != nil {
			return info, err
		}
		response = strings.TrimSpace(response)
		if response != "" && response != "?" {
			step.use(response)
		}
	}
	return info, nil
}

func (c *Client) readValue(pid byte) (float64, error) {
	reading, err := c.ReadPID(pid)
	if err != nil {
		return 0, err
	}
	if reading.Value == nil {
		return 0, fmt.Errorf("pid %02X has no numeric value", pid)
	}
	return *reading.Value, nil
}

func (c *Client) readInt(pid byte) (int, error) {
	v, err := c.readValue(pid)
	return int(v), err
}

// EngineRPM returns engine speed in RPM (PID 010C).
func (c *Client) EngineRPM() (float64, error) { return c.readValue(0x0C) }

// VehicleSpeed returns vehicle speed in km/h (PID 010D).
func (c *Client) VehicleSpeed() (int, error) { return c.readInt(0x0D) }

// EngineCoolantTemp returns coolant temperature in °C (PID 0105).
func (c *Client) EngineCoolantTemp() (int, error) { return c.readInt(0x05) }

// IntakeAirTemp returns intake air temperature in °C (PID 010F).
func (c *Client) IntakeAirTemp() (int, error) { return c.readInt(0x0F) }

// EngineLoad returns calculated engine load in percent (PID 0104).
func (c *Client) EngineLoad() (float64, error) { return c.readValue(0x04) }

// ThrottlePosition returns throttle position in percent (PID 0111).
func (c *Client) ThrottlePosition() (float64, error) { return c.readValue(0x11) }

// IntakeMAP returns intake manifold absolute pressure in kPa (PID 010B).
func (c *Client) IntakeMAP() (int, error) { return c.readInt(0x0B) }

// MAF returns mass air flow in g/s (PID 0110).
func (c *Client) MAF() (float64, error) { return c.readValue(0x10) }

// TimingAdvance returns timing advance in degrees (PID 010E).
func (c *Client) TimingAdvance() (float64, error) { return c.readValue(0x0E) }

// FuelLevel returns fuel tank level input in percent (PID 012F).
func (c *Client) FuelLevel() (float64, error) { return c.readValue(0x2F) }

// ControlModuleVoltage returns control module voltage in volts (PID 0142).
func (c *Client) ControlModuleVoltage() (float64, error) { return c.readValue(0x42) }

// RuntimeSinceEngineStart returns engine run time in seconds (PID 011F).
func (c *Client) RuntimeSinceEngineStart() (int, error) { return c.readInt(0x1F) }

// BarometricPressure returns barometric pressure in kPa (PID 0133).
func (c *Client) BarometricPressure() (int, error) { return c.readInt(0x33) }

// AmbientAirTemp returns ambient air temperature in °C (PID 0146).
func (c *Client) AmbientAirTemp() (int, error) { return c.readInt(0x46) }
