package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
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
	echo              bool
	headers           bool
	spaces            bool
	linefeed          bool
	txHeader          string // e.g. "7DF"
	canAutoFormatting bool

	searchingOnce bool
	profile       *VehicleProfile
}

type VehicleProfile struct {
	Vehicle string                `json:"vehicle"`
	PIDs    map[string]PIDProfile `json:"pids"`
}

type PIDProfile struct {
	Mode  string  `json:"mode"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Value float64 `json:"value"`
}

// NewELM327Simulator creates a new simulator instance
func NewELM327Simulator(engineStart ...time.Time) *ELM327Simulator {
	start := time.Now()
	if len(engineStart) > 0 {
		start = engineStart[0]
	}
	s := &ELM327Simulator{start: start}
	s.reset()
	return s
}

func NewELM327SimulatorWithProfile(start time.Time, profile *VehicleProfile) *ELM327Simulator {
	s := NewELM327Simulator(start)
	s.profile = profile
	return s
}

func (s *ELM327Simulator) reset() {
	s.auto = true
	s.proto = 6
	s.echo = true
	s.headers = false
	s.spaces = true
	s.linefeed = false
	s.txHeader = "7DF"
	s.canAutoFormatting = true
	s.searchingOnce = true
}

// HandleAT processes AT commands and returns appropriate responses.
func (s *ELM327Simulator) HandleAT(command string) (string, bool) {
	// Accept both "AT Z" and "ATZ" etc.
	command = strings.ToUpper(strings.TrimSpace(command))
	command = strings.ReplaceAll(command, " ", "")
	command = strings.ReplaceAll(command, "\t", "")

	echo := ""
	if s.echo {
		echo = command
	}
	respond := func(lines ...string) (string, bool) {
		if echo != "" {
			lines = append([]string{echo}, lines...)
		}
		return s.joinLines(lines...), true
	}

	switch command {
	case "ATZ":
		s.reset()
		return respond("ELM327 v1.5")
	case "ATI":
		return respond("ELM327 v1.5")
	case "ATD":
		s.reset()
		return respond("OK")
	case "ATWS":
		s.reset()
		return respond("ELM327 v1.5")
	case "ATSP0":
		s.auto = true
		s.searchingOnce = true
		return respond("OK")
	case "ATDP":
		return respond(s.describeProtocol())
	case "ATDPN":
		return respond(s.describeProtocolNumber())
	case "AT@1":
		return respond("OBDII to RS232 Interpreter")
	case "ATRV":
		v := 12.0 + rand.Float64()*1.5
		return respond(fmt.Sprintf("%.1fV", v))
	case "STI":
		return respond("STN1100 v1.2.3")
	case "VTI":
		return respond("?")
	default:
		if strings.HasPrefix(command, "ATSP") {
			arg := strings.TrimPrefix(command, "ATSP")
			auto := false
			if len(arg) == 2 && arg[0] == 'A' {
				auto, arg = true, arg[1:]
			}
			if len(arg) != 1 || !strings.ContainsRune("0123456789ABC", rune(arg[0])) {
				return respond("?")
			}
			p := parseProtoNibble(arg[0])
			if p == 0 {
				auto = true
				p = 6
			}
			s.auto = auto
			s.proto = p
			s.searchingOnce = auto
			return respond("OK")
		}

		if strings.HasPrefix(command, "AT") {
			switch command {
			case "ATE0", "ATE1":
				s.echo = command == "ATE1"
				return respond("OK")
			case "ATH0", "ATH1":
				s.headers = command == "ATH1"
				return respond("OK")
			case "ATS0", "ATS1":
				s.spaces = command == "ATS1"
				return respond("OK")
			case "ATL0", "ATL1":
				s.linefeed = command == "ATL1"
				return respond("OK")
			case "ATCAF0", "ATCAF1":
				s.canAutoFormatting = command == "ATCAF1"
				return respond("OK")
			case "ATAL", "ATNL", "ATM0", "ATM1", "ATAT0", "ATAT1", "ATAT2", "ATCFC0", "ATCFC1":
				return respond("OK")
			}
			if strings.HasPrefix(command, "ATSH") {
				header := strings.TrimPrefix(command, "ATSH")
				if (len(header) == 3 || len(header) == 8) && isHexString(header) {
					s.txHeader = header
					return respond("OK")
				}
				return respond("?")
			}
			if strings.HasPrefix(command, "ATST") {
				value := strings.TrimPrefix(command, "ATST")
				if len(value) == 2 && isHexString(value) {
					return respond("OK")
				}
			}
			return respond("?")
		}
		if strings.HasPrefix(command, "ST") {
			return respond("?")
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
	case 'C':
		return 12
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
	if p == 12 {
		return "C"
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
	case 10:
		return "SAE J1939 (CAN 29/250)"
	case 11:
		return "USER1 CAN"
	case 12:
		return "USER2 CAN"
	default:
		return "ISO 15765-4 (CAN 11/500)"
	}
}

// HandlePID generates a response for a PID request.
func (s *ELM327Simulator) HandlePID(pid string) (string, bool) {
	pid = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(pid), " ", ""))

	echo := ""
	if s.echo {
		echo = pid
	}
	respond := func(lines ...string) (string, bool) {
		if echo != "" {
			lines = append([]string{echo}, lines...)
		}
		return s.joinLines(lines...), true
	}
	if len(pid) < 4 || !isHexString(pid) {
		return respond("?")
	}

	// Minimal Mode 22 support: reply positively to DID reads like 221201.
	// Request: 22 <DID_H> <DID_L>
	// Response: 62 <DID_H> <DID_L> <data...>
	if len(pid) == 6 && strings.HasPrefix(pid, "22") {
		didHi := pid[2:4]
		didLo := pid[4:6]
		// Return 2 bytes of dummy data.
		resp := s.formatBytes([]byte{0x62, mustHexByte(didHi), mustHexByte(didLo), 0x00, 0x00})
		return respond(resp)
	}
	if len(pid) != 4 || !strings.HasPrefix(pid, "01") {
		return respond("NO DATA")
	}

	// Mode 01 responses: "41 <pidByte> <data...>"
	pidByte := pid[2:4]
	if s.profile != nil {
		return s.handleProfilePID(pid, pidByte, respond)
	}

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
			0x20, // Additional supported PID range
		})
		return respond(s.formatMode01(pidByte, []byte{a, b, c, d}))

	case "0120": // Supported PIDs [21-40]
		a, b, c, d := supportedBitmap(0x20, []byte{
			0x2F, // Fuel level
			0x33, // Barometric pressure
			0x40, // Additional supported PID range
		})
		return respond(s.formatMode01(pidByte, []byte{a, b, c, d}))

	case "0140": // Supported PIDs [41-60]
		a, b, c, d := supportedBitmap(0x40, []byte{
			0x42, // Control module voltage
			0x46, // Ambient air temperature
		})
		return respond(s.formatMode01(pidByte, []byte{a, b, c, d}))

	case "010C": // Engine RPM: ((A*256)+B)/4
		rpm := rand.Intn(3500) + 700 // 700-4200
		v := rpm * 4
		a := (v >> 8) & 0xFF
		b := v & 0xFF
		return respond(s.formatMode01(pidByte, []byte{byte(a), byte(b)}))

	case "010D": // Vehicle speed: A (km/h)
		speed := rand.Intn(121) // 0-120
		return respond(s.formatMode01(pidByte, []byte{byte(speed)}))

	case "0105": // Coolant temp: A-40
		tempC := rand.Intn(61) + 60 // 60-120
		return respond(s.formatMode01(pidByte, []byte{byte(tempC + 40)}))

	case "010F": // Intake air temp: A-40
		tempC := rand.Intn(51) + 10 // 10-60
		return respond(s.formatMode01(pidByte, []byte{byte(tempC + 40)}))

	case "0146": // Ambient air temp: A-40
		tempC := rand.Intn(46) + 5 // 5-50
		return respond(s.formatMode01(pidByte, []byte{byte(tempC + 40)}))

	case "0104": // Engine load: (A*100)/255
		loadPct := rand.Intn(81) // 0-80
		a := int(float64(loadPct) * 255.0 / 100.0)
		return respond(s.formatMode01(pidByte, []byte{byte(a)}))

	case "0111": // Throttle position: (A*100)/255
		throttlePct := rand.Intn(71) // 0-70
		a := int(float64(throttlePct) * 255.0 / 100.0)
		return respond(s.formatMode01(pidByte, []byte{byte(a)}))

	case "010B": // MAP kPa: A
		kpa := rand.Intn(71) + 30 // 30-100
		return respond(s.formatMode01(pidByte, []byte{byte(kpa)}))

	case "0110": // MAF: ((A*256)+B)/100 g/s
		maf := rand.Float64()*25.0 + 2.0 // 2-27 g/s
		v := int(maf * 100.0)
		a := (v >> 8) & 0xFF
		b := v & 0xFF
		return respond(s.formatMode01(pidByte, []byte{byte(a), byte(b)}))

	case "010E": // Timing advance: (A/2)-64 degrees
		deg := rand.Float64()*40.0 - 5.0 // -5 to 35
		a := int((deg + 64.0) * 2.0)
		if a < 0 {
			a = 0
		}
		if a > 255 {
			a = 255
		}
		return respond(s.formatMode01(pidByte, []byte{byte(a)}))

	case "012F": // Fuel level (%). Some vehicles don't support; here provide a value.
		fuelPct := rand.Intn(101) // 0-100
		a := int(float64(fuelPct) * 255.0 / 100.0)
		return respond(s.formatMode01(pidByte, []byte{byte(a)}))

	case "0142": // Control module voltage: ((A*256)+B)/1000 V
		v := rand.Float64()*1.5 + 12.0 // 12.0-13.5
		raw := int(v * 1000.0)
		a := (raw >> 8) & 0xFF
		b := raw & 0xFF
		return respond(s.formatMode01(pidByte, []byte{byte(a), byte(b)}))

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
		return respond(s.formatMode01(pidByte, []byte{byte(a), byte(b)}))

	case "0133": // Barometric pressure kPa: A
		kpa := rand.Intn(31) + 80 // 80-110
		return respond(s.formatMode01(pidByte, []byte{byte(kpa)}))

	default:
		return respond("NO DATA")
	}
}

func (s *ELM327Simulator) handleProfilePID(pid, pidByte string, respond func(...string) (string, bool)) (string, bool) {
	if pid == "0100" || pid == "0120" || pid == "0140" {
		if cfg, exists := s.profile.PIDs[pid]; exists && cfg.Mode == "unsupported" {
			return respond("NO DATA")
		}
		baseValue, _ := strconv.ParseUint(pid[2:4], 16, 8)
		base := byte(baseValue)
		var supported []byte
		for command, cfg := range s.profile.PIDs {
			if cfg.Mode == "unsupported" || len(command) != 4 || !strings.HasPrefix(command, "01") || !isHexString(command) {
				continue
			}
			value, _ := strconv.ParseUint(command[2:4], 16, 8)
			supported = append(supported, byte(value))
		}
		// Advertise the next range when the profile contains a PID in it.
		for _, value := range supported {
			if value > base+0x20 {
				supported = append(supported, base+0x20)
				break
			}
		}
		a, b, c, d := supportedBitmap(base, supported)
		return respond(s.formatMode01(pidByte, []byte{a, b, c, d}))
	}

	cfg, exists := s.profile.PIDs[pid]
	if !exists || cfg.Mode == "unsupported" {
		return respond("NO DATA")
	}
	value := cfg.Value
	switch cfg.Mode {
	case "fixed":
	case "random":
		value = cfg.Min + rand.Float64()*(cfg.Max-cfg.Min)
	default:
		return respond("NO DATA")
	}
	data, ok := encodeProfileValue(pid, value)
	if !ok {
		return respond("NO DATA")
	}
	return respond(s.formatMode01(pidByte, data))
}

func encodeProfileValue(pid string, value float64) ([]byte, bool) {
	byteValue := func(v float64) []byte {
		v = max(0, min(255, v))
		return []byte{byte(v)}
	}
	wordValue := func(v float64) []byte {
		v = max(0, min(65535, v))
		raw := int(v)
		return []byte{byte(raw >> 8), byte(raw)}
	}
	switch pid {
	case "0104", "0111", "012F":
		return byteValue(value * 255 / 100), true
	case "0105", "010F", "0146":
		return byteValue(value + 40), true
	case "010B", "010D", "011C", "0133":
		return byteValue(value), true
	case "010C":
		return wordValue(value * 4), true
	case "010E":
		return byteValue((value + 64) * 2), true
	case "0110":
		return wordValue(value * 100), true
	case "011F", "0131":
		return wordValue(value), true
	case "0142":
		return wordValue(value * 1000), true
	default:
		return nil, false
	}
}

// Handle implements the elm327.HandlerFunc signature by combining HandleAT and HandlePID.
func (s *ELM327Simulator) Handle(command string) (string, bool) {
	command = strings.ToUpper(strings.TrimSpace(command))
	normalized := strings.ReplaceAll(command, " ", "")

	if strings.HasPrefix(normalized, "AT") || strings.HasPrefix(normalized, "ST") || strings.HasPrefix(normalized, "VT") {
		return s.HandleAT(command)
	}
	if len(normalized) >= 4 {
		return s.HandlePID(normalized)
	}
	return "", false
}

func (s *ELM327Simulator) joinLines(lines ...string) string {
	sep := "\r"
	if s.linefeed {
		sep = "\r\n"
	}
	nonEmpty := lines[:0]
	for _, line := range lines {
		if line != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	return strings.Join(nonEmpty, sep) + sep
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

	// Build the application payload: 41 <pidByte> <data...>.
	payload := make([]byte, 0, 2+len(data))
	payload = append(payload, 0x41, mustHexByte(pidByte))
	payload = append(payload, data...)

	if !s.canAutoFormatting {
		payload = append([]byte{byte(len(payload))}, payload...)
	}

	if !s.headers {
		return prefix + s.formatBytes(payload)
	}

	rxHeader := s.responseHeader()
	if s.canAutoFormatting {
		payload = append([]byte{byte(len(payload))}, payload...)
	}
	line := rxHeader + s.formatBytesNoPrefix(payload)
	if s.spaces {
		return prefix + rxHeader + " " + s.formatBytes(payload)
	}
	return prefix + line
}

func (s *ELM327Simulator) responseHeader() string {
	if len(s.txHeader) == 3 && strings.HasPrefix(s.txHeader, "7E") {
		address, err := strconv.ParseUint(s.txHeader, 16, 16)
		if err == nil && address >= 0x7E0 && address <= 0x7E7 {
			return fmt.Sprintf("%03X", address+8)
		}
	}
	if len(s.txHeader) == 8 && strings.HasPrefix(s.txHeader, "18DA") && strings.HasSuffix(s.txHeader, "F1") {
		return "18DAF1" + s.txHeader[4:6]
	}
	return "7E8"
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

func isHexString(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if !((ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
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
	var profilePath string

	flag.IntVar(&port, "port", 35000, "tcp port")
	flag.StringVar(&profilePath, "profile", "", "optional vehicle profile JSON")
	flag.Parse()

	var profile *VehicleProfile
	if profilePath != "" {
		loaded, err := loadProfile(profilePath)
		if err != nil {
			log.Fatalf("Load profile failed: %v", err)
		}
		profile = loaded
		log.Printf("Loaded vehicle profile %q", profile.Vehicle)
	}
	engineStart := time.Now()
	err := elm327.ListenAndServeSessions(fmt.Sprintf("0.0.0.0:%d", port), func() elm327.HandlerFunc {
		return NewELM327SimulatorWithProfile(engineStart, profile).Handle
	})
	if err != nil {
		log.Fatalf("Listen and serve failed: %v", err)
	}
}

func loadProfile(path string) (*VehicleProfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var profile VehicleProfile
	if err := decoder.Decode(&profile); err != nil {
		return nil, err
	}
	if profile.Vehicle == "" || len(profile.PIDs) == 0 {
		return nil, fmt.Errorf("profile requires vehicle and pids")
	}
	for command, cfg := range profile.PIDs {
		if len(command) != 4 || !strings.HasPrefix(command, "01") || !isHexString(command) {
			return nil, fmt.Errorf("invalid profile PID %q", command)
		}
		if cfg.Mode != "fixed" && cfg.Mode != "random" && cfg.Mode != "unsupported" {
			return nil, fmt.Errorf("PID %s has invalid mode %q", command, cfg.Mode)
		}
		if cfg.Mode == "random" && cfg.Max < cfg.Min {
			return nil, fmt.Errorf("PID %s has max below min", command)
		}
	}
	return &profile, nil
}
