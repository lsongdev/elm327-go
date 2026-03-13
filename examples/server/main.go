package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/lsongdev/elm327-go/elm327"
)

// ELM327Simulator represents the main simulator instance
type ELM327Simulator struct {
	start time.Time
	auto  bool
	proto byte // current protocol number when known

	// ELM formatting/settings (subset).
	echo     bool
	headers  bool
	spaces   bool
	linefeed bool
	txHeader string // e.g. "7DF"

	searchingOnce bool
}

// NewELM327Simulator creates a new simulator instance
func NewELM327Simulator() *ELM327Simulator {
	return &ELM327Simulator{
		start: time.Now(),
		auto:  true,
		proto: 6, // default to CAN 11/500
		echo:  true,
		// Many apps set these explicitly during init; defaults keep things readable.
		headers:       false,
		spaces:        true,
		linefeed:      false,
		txHeader:      "7DF",
		searchingOnce: true,
	}
}

// HandleAT processes AT commands and returns appropriate responses.
func (s *ELM327Simulator) HandleAT(command string) (string, bool) {
	// Accept both "AT Z" and "ATZ" etc.
	command = strings.ToUpper(strings.TrimSpace(command))
	command = strings.ReplaceAll(command, " ", "")
	command = strings.ReplaceAll(command, "\t", "")

	orig := command
	if s.echo {
		// Echo back the command line.
		// Real devices may echo even when ATE0 is being processed; this is "good enough".
		orig = orig + "\r"
	} else {
		orig = ""
	}

	switch command {
	case "ATZ", "ATI":
		// Reset a few settings to defaults.
		s.echo = true
		s.headers = false
		s.spaces = true
		s.linefeed = false
		s.auto = true
		s.proto = 6
		s.txHeader = "7DF"
		s.searchingOnce = true
		return s.joinLines(orig, "", "ELM327 v1.5", ""), true
	case "ATSP0":
		s.auto = true
		s.searchingOnce = true
		return s.joinLines(orig, "OK"), true
	case "ATDP":
		return s.joinLines(orig, s.describeProtocol()), true
	case "ATDPN":
		return s.joinLines(orig, s.describeProtocolNumber()), true
	case "AT@1":
		return s.joinLines(orig, "OBDII to RS232 Interpreter"), true
	case "ATRV":
		// Return a plausible battery voltage string.
		v := 12.0 + rand.Float64()*1.5
		return s.joinLines(orig, fmt.Sprintf("%.1fV", v)), true
	case "STI":
		// OBD Fusion often probes STN-based adapters with ST commands.
		// Match the log you captured.
		return s.joinLines(orig, "STN1100 v1.2.3"), true
	case "VTI":
		// Some apps probe generic adapter info with VT* commands.
		// Real adapter replies '?' in your capture.
		return s.joinLines(orig, "?"), true
	default:
		if strings.HasPrefix(command, "ATSP") && len(command) >= 5 {
			// Select protocol. Accept hex digit after ATSP.
			n := command[4:]
			// Some apps send ATSP6, others ATSP06; take last nibble.
			last := n[len(n)-1]
			switch last {
			case '0':
				s.auto = true
				s.searchingOnce = true
				return s.joinLines(orig, "OK"), true
			case '1', '2', '3', '4', '5', '6', '7', '8', '9', 'A', 'B':
				s.auto = false
				s.proto = parseProtoNibble(last)
				return s.joinLines(orig, "OK"), true
			}
			return s.joinLines(orig, "?"), true
		}

		if strings.HasPrefix(command, "AT") {
			// Handle a subset of common init commands with real side effects.
			switch {
			case strings.HasPrefix(command, "ATE") && len(command) >= 4:
				s.echo = command[len(command)-1] != '0'
				return s.joinLines(orig, "OK"), true
			case strings.HasPrefix(command, "ATH") && len(command) >= 4:
				s.headers = command[len(command)-1] != '0'
				return s.joinLines(orig, "OK"), true
			case strings.HasPrefix(command, "ATS") && len(command) >= 4:
				s.spaces = command[len(command)-1] != '0'
				return s.joinLines(orig, "OK"), true
			case strings.HasPrefix(command, "ATL") && len(command) >= 4:
				s.linefeed = command[len(command)-1] != '0'
				return s.joinLines(orig, "OK"), true
			case strings.HasPrefix(command, "ATSH") && len(command) == 7:
				s.txHeader = command[4:]
				return s.joinLines(orig, "OK"), true
			default:
				// Accept other AT* during init.
				return s.joinLines(orig, "OK"), true
			}
		}
		if strings.HasPrefix(command, "ST") {
			// Treat other ST* commands as accepted to avoid repeated probing.
			return s.joinLines(orig, "OK"), true
		}
		return "", false
	}
}

func parseProtoNibble(ch byte) byte {
	switch ch {
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return ch - '0'
	case 'A':
		return 10
	case 'B':
		return 11
	default:
		return 0
	}
}

func (s *ELM327Simulator) describeProtocol() string {
	// Minimal set to satisfy common apps.
	name := protocolName(s.proto)
	if s.auto {
		return "AUTO, " + name
	}
	return name
}

func (s *ELM327Simulator) describeProtocolNumber() string {
	// ELM: if auto mode, prefix with 'A' + protocol digit.
	p := protocolDigit(s.proto)
	if s.auto {
		return "A" + p
	}
	return p
}

func protocolDigit(p byte) string {
	if p <= 9 {
		return fmt.Sprintf("%d", p)
	}
	// Only A/B are used in our parser above.
	if p == 10 {
		return "A"
	}
	if p == 11 {
		return "B"
	}
	return "0"
}

func protocolName(p byte) string {
	switch p {
	case 1:
		return "SAE J1850 PWM"
	case 2:
		return "SAE J1850 VPW"
	case 3:
		return "ISO 9141-2"
	case 4:
		return "ISO 14230-4 (KWP 5BAUD)"
	case 5:
		return "ISO 14230-4 (KWP FAST)"
	case 6:
		return "ISO 15765-4 (CAN 11/500)"
	case 7:
		return "ISO 15765-4 (CAN 29/500)"
	case 8:
		return "ISO 15765-4 (CAN 11/250)"
	case 9:
		return "ISO 15765-4 (CAN 29/250)"
	default:
		return "ISO 15765-4 (CAN 11/500)"
	}
}

// HandlePID generates a response for a PID request.
func (s *ELM327Simulator) HandlePID(pid string) (string, bool) {
	pid = strings.ToUpper(strings.TrimSpace(pid))
	if len(pid) < 4 {
		return "NO DATA", true
	}

	orig := pid
	if s.echo {
		orig = orig + "\r"
	} else {
		orig = ""
	}

	// Minimal Mode 22 support: reply positively to DID reads like 221201.
	// Request: 22 <DID_H> <DID_L>
	// Response: 62 <DID_H> <DID_L> <data...>
	if len(pid) == 6 && strings.HasPrefix(pid, "22") {
		didHi := pid[2:4]
		didLo := pid[4:6]
		// Return 2 bytes of dummy data.
		resp := s.formatBytes([]byte{0x62, mustHexByte(didHi), mustHexByte(didLo), 0x00, 0x00})
		return s.joinLines(orig, resp), true
	}

	// Mode 01 responses: "41 <pidByte> <data...>"
	pidByte := pid[2:4]

	switch pid {
	case "0100": // Supported PIDs [01-20]
		a, b, c, d := supportedBitmap(0x00, []byte{
			0x04, // Engine load
			0x05, // Coolant temp
			0x0B, // MAP
			0x0C, // RPM
			0x0D, // Speed
			0x0E, // Timing advance
			0x0F, // Intake air temp
			0x10, // MAF
			0x11, // Throttle position
			0x1F, // Runtime since engine start
		})
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{a, b, c, d})), true

	case "0120": // Supported PIDs [21-40]
		a, b, c, d := supportedBitmap(0x20, []byte{
			0x2F, // Fuel level
			0x33, // Barometric pressure
		})
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{a, b, c, d})), true

	case "0140": // Supported PIDs [41-60]
		a, b, c, d := supportedBitmap(0x40, []byte{
			0x42, // Control module voltage
			0x46, // Ambient air temperature
		})
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{a, b, c, d})), true

	case "010C": // Engine RPM: ((A*256)+B)/4
		rpm := rand.Intn(3500) + 700 // 700-4200
		v := rpm * 4
		a := (v >> 8) & 0xFF
		b := v & 0xFF
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(a), byte(b)})), true

	case "010D": // Vehicle speed: A (km/h)
		speed := rand.Intn(121) // 0-120
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(speed)})), true

	case "0105": // Coolant temp: A-40
		tempC := rand.Intn(61) + 60 // 60-120
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(tempC + 40)})), true

	case "010F": // Intake air temp: A-40
		tempC := rand.Intn(51) + 10 // 10-60
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(tempC + 40)})), true

	case "0146": // Ambient air temp: A-40
		tempC := rand.Intn(46) + 5 // 5-50
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(tempC + 40)})), true

	case "0104": // Engine load: (A*100)/255
		loadPct := rand.Intn(81) // 0-80
		a := int(float64(loadPct) * 255.0 / 100.0)
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(a)})), true

	case "0111": // Throttle position: (A*100)/255
		throttlePct := rand.Intn(71) // 0-70
		a := int(float64(throttlePct) * 255.0 / 100.0)
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(a)})), true

	case "010B": // MAP kPa: A
		kpa := rand.Intn(71) + 30 // 30-100
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(kpa)})), true

	case "0110": // MAF: ((A*256)+B)/100 g/s
		maf := rand.Float64()*25.0 + 2.0 // 2-27 g/s
		v := int(maf * 100.0)
		a := (v >> 8) & 0xFF
		b := v & 0xFF
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(a), byte(b)})), true

	case "010E": // Timing advance: (A/2)-64 degrees
		deg := rand.Float64()*40.0 - 5.0 // -5 to 35
		a := int((deg + 64.0) * 2.0)
		if a < 0 {
			a = 0
		}
		if a > 255 {
			a = 255
		}
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(a)})), true

	case "012F": // Fuel level (%). Some vehicles don't support; here provide a value.
		fuelPct := rand.Intn(101) // 0-100
		a := int(float64(fuelPct) * 255.0 / 100.0)
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(a)})), true

	case "0142": // Control module voltage: ((A*256)+B)/1000 V
		v := rand.Float64()*1.5 + 12.0 // 12.0-13.5
		raw := int(v * 1000.0)
		a := (raw >> 8) & 0xFF
		b := raw & 0xFF
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(a), byte(b)})), true

	case "011F": // Runtime since engine start: (A*256)+B seconds
		sec := int(time.Since(s.start).Seconds())
		if sec < 0 {
			sec = 0
		}
		if sec > 0xFFFF {
			sec = 0xFFFF
		}
		a := (sec >> 8) & 0xFF
		b := sec & 0xFF
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(a), byte(b)})), true

	case "0133": // Barometric pressure kPa: A
		kpa := rand.Intn(31) + 80 // 80-110
		return s.joinLines(orig, s.formatMode01(pidByte, []byte{byte(kpa)})), true

	default:
		return s.joinLines(orig, "NO DATA"), true
	}
}

func (s *ELM327Simulator) joinLines(lines ...string) string {
	sep := "\r"
	if s.linefeed {
		sep = "\r\n"
	}
	// Filter out empty lines only when they are truly empty; we still want explicit blank lines.
	return strings.Join(lines, sep)
}

func (s *ELM327Simulator) formatMode01(pidByte string, data []byte) string {
	// Optionally include SEARCHING... once after selecting AUTO protocol.
	prefix := ""
	if s.auto && s.searchingOnce {
		s.searchingOnce = false
		prefix = "SEARCHING...\r"
		if s.linefeed {
			prefix = "SEARCHING...\r\n"
		}
	}

	// Build payload: 41 <pidByte> <data...>
	payload := make([]byte, 0, 2+len(data))
	payload = append(payload, 0x41, mustHexByte(pidByte))
	payload = append(payload, data...)

	if !s.headers {
		return prefix + s.formatBytes(payload)
	}

	// CAN single-frame style line: <rxHeader><len><payload>
	// In your capture (ATS0) it is rendered without spaces, e.g. 7E8 06 41 00 ...
	rxHeader := "7E8"
	line := rxHeader + fmt.Sprintf("%02X", len(payload)) + s.formatBytesNoPrefix(payload)
	if s.spaces {
		// With spaces, make it look like: 7E8 06 41 00 ...
		return prefix + rxHeader + " " + fmt.Sprintf("%02X", len(payload)) + " " + s.formatBytes(payload)
	}
	return prefix + line
}

func (s *ELM327Simulator) formatBytes(b []byte) string {
	if s.spaces {
		parts := make([]string, 0, len(b))
		for _, v := range b {
			parts = append(parts, fmt.Sprintf("%02X", v))
		}
		return strings.Join(parts, " ")
	}
	return s.formatBytesNoPrefix(b)
}

func (s *ELM327Simulator) formatBytesNoPrefix(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) * 2)
	for _, v := range b {
		sb.WriteString(fmt.Sprintf("%02X", v))
	}
	return sb.String()
}

func mustHexByte(twoHex string) byte {
	if len(twoHex) != 2 {
		return 0
	}
	v, err := strconv.ParseUint(twoHex, 16, 8)
	if err != nil {
		return 0
	}
	return byte(v)
}

// supportedBitmap builds the 4-byte bitmap used by PID 0100/0120/0140 etc.
// base must be 0x00, 0x20, 0x40 ... indicating the PID range start.
// pids are absolute PID numbers (e.g. 0x0C, 0x2F).
func supportedBitmap(base byte, pids []byte) (byte, byte, byte, byte) {
	var bits uint32
	for _, pid := range pids {
		if pid <= base || pid > base+0x20 {
			continue
		}
		pos := pid - base // 1..32
		// bit 31 corresponds to PID base+1, bit 0 to base+32
		shift := 32 - uint(pos)
		bits |= 1 << shift
	}
	return byte(bits >> 24), byte(bits >> 16), byte(bits >> 8), byte(bits)
}

func main() {
	var port int

	flag.IntVar(&port, "port", 35000, "tcp port")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())
	h := NewELM327Simulator()
	err := elm327.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", port), h)
	if err != nil {
		log.Fatalf("Listen and serve failed: %v", err)
	}
}
