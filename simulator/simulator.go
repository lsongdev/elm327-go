// Package simulator emulates an ELM327 adapter plugged into a running vehicle,
// for developing OBD clients away from a car. It speaks the elm327.Server
// handler contract and implements the common AT commands plus OBD services 01,
// 02, 03, 04, 07, 09 and 0A over CAN, with multi-frame responses.
//
// It does not model bus timing, electrical faults or non-CAN framing.
package simulator

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lsongdev/elm327-go/elm327"
	"github.com/lsongdev/elm327-go/obd"
)

// Profile describes a vehicle. PIDs not listed answer NO DATA.
type Profile struct {
	Vehicle   string                `json:"vehicle"`
	VIN       string                `json:"vin,omitempty"`
	Stored    []string              `json:"stored,omitempty"`    // Mode 03 codes
	Pending   []string              `json:"pending,omitempty"`   // Mode 07 codes
	Permanent []string              `json:"permanent,omitempty"` // Mode 0A codes
	PIDs      map[string]PIDProfile `json:"pids"`
}

// PIDProfile sets how a PID behaves: "fixed" at Value, "random" between Min and
// Max, or "unsupported".
type PIDProfile struct {
	Mode  string  `json:"mode"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Value float64 `json:"value"`
}

// LoadProfile reads and validates a profile JSON file.
func LoadProfile(path string) (*Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var profile Profile
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
	for _, codes := range [][]string{profile.Stored, profile.Pending, profile.Permanent} {
		for _, code := range codes {
			if _, err := obd.EncodeDTC(code); err != nil {
				return nil, err
			}
		}
	}
	return &profile, nil
}

// Vehicle is the state every connection to the adapter shares: what the engine
// is doing and which trouble codes are set. Formatting state is per Session.
type Vehicle struct {
	mu        sync.Mutex
	start     time.Time
	now       func() time.Time
	profile   *Profile
	stored    []string
	pending   []string
	permanent []string
	cleared   bool
}

// NewVehicle starts a vehicle whose engine starts now. A nil profile gives a
// built-in car driving a repeating cycle, with a misfire and a catalyst fault.
func NewVehicle(profile *Profile) *Vehicle {
	v := &Vehicle{start: time.Now(), now: time.Now, profile: profile}
	if profile == nil {
		v.stored = []string{"P0301", "P0420"}
		v.pending = []string{"P0171"}
		v.permanent = []string{"P0420"}
	} else {
		v.stored = slices.Clone(profile.Stored)
		v.pending = slices.Clone(profile.Pending)
		v.permanent = slices.Clone(profile.Permanent)
	}
	return v
}

// Serve answers ELM327 connections on l until l is closed. A nil logger discards.
func (v *Vehicle) Serve(l net.Listener, logger *log.Logger) error {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	server := &elm327.Server{
		NewHandler: func() elm327.HandlerFunc { return v.NewSession().Handle },
		Logger:     logger,
	}
	return server.Serve(l)
}

// Session is one connection's view of the adapter.
type Session struct {
	vehicle *Vehicle
	auto    bool
	proto   byte // current protocol number when known

	// ELM formatting/settings (subset).
	echo              bool
	headers           bool
	spaces            bool
	linefeed          bool
	txHeader          string // e.g. "7DF"
	canAutoFormatting bool

	searchingOnce bool
}

// NewSession returns an adapter in its power-on state.
func (v *Vehicle) NewSession() *Session {
	s := &Session{vehicle: v}
	s.reset()
	return s
}

func (s *Session) reset() {
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

// Handle implements elm327.HandlerFunc.
func (s *Session) Handle(command string) (string, bool) {
	command = strings.ToUpper(strings.TrimSpace(command))
	normalized := strings.ReplaceAll(command, " ", "")

	if strings.HasPrefix(normalized, "AT") || strings.HasPrefix(normalized, "ST") || strings.HasPrefix(normalized, "VT") {
		return s.handleAT(command)
	}
	if len(normalized) >= 2 {
		return s.handleOBD(normalized)
	}
	return "", false
}

// handleAT processes AT commands and returns appropriate responses.
func (s *Session) handleAT(command string) (string, bool) {
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
		return respond(fmt.Sprintf("%.1fV", 12.0+rand.Float64()*1.5))
	case "STI":
		return respond("STN1100 v1.2.3")
	case "VTI":
		return respond("?")
	}

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
		case "ATAL", "ATNL", "ATM0", "ATM1", "ATAT0", "ATAT1", "ATAT2", "ATCFC0", "ATCFC1", "ATD0", "ATD1":
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

func (s *Session) describeProtocol() string {
	// Minimal set to satisfy common apps.
	name := protocolName(s.proto)
	if s.auto {
		return "AUTO, " + name
	}
	return name
}

func (s *Session) describeProtocolNumber() string {
	// ELM: if auto mode, prefix with 'A' + protocol digit.
	p := protocolDigit(s.proto)
	if s.auto {
		return "A" + p
	}
	return p
}

func protocolDigit(p byte) string {
	if p <= 12 {
		return strings.ToUpper(strconv.FormatInt(int64(p), 16))
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

// handleOBD answers an OBD request of hex digits.
func (s *Session) handleOBD(request string) (string, bool) {
	var lines []string
	if s.echo {
		lines = append(lines, request)
	}
	finish := func(more ...string) (string, bool) {
		return s.joinLines(append(lines, more...)...), true
	}
	if len(request)%2 != 0 || !isHexString(request) {
		return finish("?")
	}
	req := make([]byte, len(request)/2)
	for i := range req {
		req[i] = mustHexByte(request[i*2 : i*2+2])
	}
	payload := s.vehicle.answer(req)
	if payload == nil {
		return finish("NO DATA")
	}
	return finish(s.formatMessage(payload)...)
}

// answer returns the positive response to a request, or nil for NO DATA.
func (v *Vehicle) answer(req []byte) []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	service := req[0]
	switch service {
	case 0x01:
		if len(req) != 2 {
			return nil
		}
		data := v.currentData(req[1])
		if data == nil {
			return nil
		}
		return append([]byte{0x41, req[1]}, data...)
	case 0x02:
		if len(req) != 3 {
			return nil
		}
		data := v.freezeData(req[1])
		if data == nil {
			return nil
		}
		return append([]byte{0x42, req[1], req[2]}, data...)
	case byte(obd.DTCStored):
		return encodeDTCResponse(0x43, v.stored)
	case byte(obd.DTCPending):
		return encodeDTCResponse(0x47, v.pending)
	case byte(obd.DTCPermanent):
		return encodeDTCResponse(0x4A, v.permanent)
	case 0x04:
		v.stored, v.pending = nil, nil // permanent codes survive a clear
		v.cleared = true
		return []byte{0x44}
	case 0x09:
		if len(req) != 2 {
			return nil
		}
		data := v.infoData(req[1])
		if data == nil {
			return nil
		}
		return append([]byte{0x49, req[1]}, data...)
	case 0x22:
		// Minimal support: reply positively to DID reads like 221201.
		if len(req) == 3 {
			return []byte{0x62, req[1], req[2], 0x00, 0x00}
		}
	}
	return nil
}

func encodeDTCResponse(service byte, codes []string) []byte {
	out := []byte{service, byte(len(codes))}
	for _, code := range codes {
		if b, err := obd.EncodeDTC(code); err == nil {
			out = append(out, b...)
		}
	}
	return out
}

// infoData answers Mode 09. Callers hold v.mu.
func (v *Vehicle) infoData(infoType byte) []byte {
	vin, calibration, cvn, name := defaultVIN, "SIM-CAL-00000001", []byte{0x12, 0xAB, 0x34, 0xCD}, "ECM-EngineControl"
	if v.profile != nil {
		if v.profile.VIN == "" {
			return nil
		}
		vin = v.profile.VIN
	}
	switch infoType {
	case obd.InfoSupported:
		return obd.EncodeSupported(0x00, []byte{obd.InfoVIN, obd.InfoCalibrationID, obd.InfoCVN, obd.InfoECUName})
	case obd.InfoVIN:
		return append([]byte{0x01}, vin...)
	case obd.InfoCalibrationID:
		return append([]byte{0x01}, fmt.Sprintf("%-16s", calibration)...)
	case obd.InfoCVN:
		return append([]byte{0x01}, cvn...)
	case obd.InfoECUName:
		return append([]byte{0x01}, fmt.Sprintf("%-20s", name)...)
	}
	return nil
}

// currentData answers Mode 01. Callers hold v.mu.
func (v *Vehicle) currentData(pid byte) []byte {
	if obd.IsBitmap(pid) {
		if v.profile != nil {
			if cfg, ok := v.profile.PIDs[fmt.Sprintf("01%02X", pid)]; ok && cfg.Mode == "unsupported" {
				return nil
			}
		}
		return bitmapFor(pid, v.supported())
	}
	if v.profile != nil {
		return v.profileData(pid)
	}
	return v.driveData(pid, v.state(v.now()))
}

// bitmapFor advertises the PIDs of one range, and the next range while any PID
// lies beyond it.
func bitmapFor(base byte, pids []byte) []byte {
	list := slices.Clone(pids)
	for _, pid := range pids {
		if int(pid) > int(base)+0x20 {
			list = append(list, base+0x20)
			break
		}
	}
	return obd.EncodeSupported(base, list)
}

func (v *Vehicle) supported() []byte {
	if v.profile == nil {
		return drivePIDs
	}
	var out []byte
	for command, cfg := range v.profile.PIDs {
		if cfg.Mode == "unsupported" {
			continue
		}
		out = append(out, mustHexByte(command[2:4]))
	}
	slices.Sort(out)
	return out
}

func (v *Vehicle) profileData(pid byte) []byte {
	cfg, ok := v.profile.PIDs[fmt.Sprintf("01%02X", pid)]
	if !ok {
		return nil
	}
	value := cfg.Value
	switch cfg.Mode {
	case "fixed":
	case "random":
		value = cfg.Min + rand.Float64()*(cfg.Max-cfg.Min)
	default:
		return nil
	}
	return encodeValue(pid, value)
}

// freezeData answers Mode 02 from the moment the first stored code was set.
// Callers hold v.mu.
func (v *Vehicle) freezeData(pid byte) []byte {
	if pid == 0x02 {
		if len(v.stored) == 0 {
			return []byte{0, 0}
		}
		b, err := obd.EncodeDTC(v.stored[0])
		if err != nil {
			return []byte{0, 0}
		}
		return b
	}
	if len(v.stored) == 0 || v.profile != nil {
		return nil
	}
	if obd.IsBitmap(pid) {
		return bitmapFor(pid, freezePIDs)
	}
	if !slices.Contains(freezePIDs, pid) {
		return nil
	}
	return v.driveData(pid, v.state(v.start.Add(95*time.Second)))
}

// formatMessage renders a service response under the current formatting.
func (s *Session) formatMessage(payload []byte) []string {
	var lines []string
	if s.auto && s.searchingOnce {
		s.searchingOnce = false
		lines = append(lines, "SEARCHING...")
	}
	header := ""
	if s.headers {
		header = s.responseHeader()
		if s.spaces {
			header += " "
		}
	}
	line := func(frame []byte) string { return header + s.formatBytes(frame) }

	if len(payload) <= 7 {
		if !s.headers && s.canAutoFormatting {
			return append(lines, s.formatBytes(payload))
		}
		return append(lines, line(append([]byte{byte(len(payload))}, payload...)))
	}

	// Multi-frame. With headers off, an ELM327 numbers the lines itself.
	numbered := !s.headers && s.canAutoFormatting
	if numbered {
		lines = append(lines, fmt.Sprintf("%03X", len(payload)))
		first, rest := payload[:6], payload[6:]
		lines = append(lines, "0: "+s.formatBytes(first))
		for i := 1; len(rest) > 0; i++ {
			n := min(7, len(rest))
			lines = append(lines, fmt.Sprintf("%X: %s", i%16, s.formatBytes(rest[:n])))
			rest = rest[n:]
		}
		return lines
	}
	lines = append(lines, line(append([]byte{0x10 | byte(len(payload)>>8), byte(len(payload))}, payload[:6]...)))
	rest := payload[6:]
	for i := 1; len(rest) > 0; i++ {
		n := min(7, len(rest))
		lines = append(lines, line(append([]byte{0x20 | byte(i%16)}, rest[:n]...)))
		rest = rest[n:]
	}
	return lines
}

func (s *Session) responseHeader() string {
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

func (s *Session) joinLines(lines ...string) string {
	sep := "\r"
	if s.linefeed {
		sep = "\r\n"
	}
	var nonEmpty []string
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

func (s *Session) formatBytes(b []byte) string {
	sep := ""
	if s.spaces {
		sep = " "
	}
	parts := make([]string, 0, len(b))
	for _, v := range b {
		parts = append(parts, fmt.Sprintf("%02X", v))
	}
	return strings.Join(parts, sep)
}

func mustHexByte(twoHex string) byte {
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
