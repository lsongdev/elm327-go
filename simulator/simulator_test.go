package simulator

import (
	"math"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lsongdev/elm327-go/elm327"
	"github.com/lsongdev/elm327-go/obd"
)

func TestATIHasNoResetSideEffect(t *testing.T) {
	s := NewVehicle(nil).NewSession()
	if response, _ := s.handleAT("ATE0"); response != "ATE0\rOK\r" {
		t.Fatalf("ATE0 response = %q", response)
	}
	response, _ := s.handleAT("ATI")
	if response != "ELM327 v1.5\r" {
		t.Fatalf("ATI response = %q", response)
	}
	if s.echo {
		t.Fatal("ATI unexpectedly reset echo")
	}
}

func TestUnknownAndInvalidATCommands(t *testing.T) {
	s := NewVehicle(nil).NewSession()
	for _, command := range []string{"ATMADEUP", "ATE9", "ATSP06", "ATSHXYZ"} {
		response, handled := s.handleAT(command)
		if !handled || !strings.Contains(response, "?\r") {
			t.Fatalf("%s response = %q, handled=%v", command, response, handled)
		}
	}
}

func TestSupportedPIDRangeBits(t *testing.T) {
	s := NewVehicle(nil).NewSession()
	s.echo = false
	s.auto = false
	for _, request := range []string{"0100", "0120", "0140", "0160", "0180"} {
		response, _ := s.handleOBD(request)
		if !hasNextRangeBit(response) {
			t.Fatalf("%s does not advertise the next range: %q", request, response)
		}
	}
	if response, _ := s.handleOBD("01A0"); hasNextRangeBit(response) {
		t.Fatalf("01A0 advertises a range that has no PIDs: %q", response)
	}
}

func hasNextRangeBit(response string) bool {
	fields := strings.Fields(response)
	if len(fields) < 6 {
		return false
	}
	last, err := strconv.ParseUint(fields[len(fields)-1], 16, 8)
	return err == nil && last&1 != 0
}

func TestFormattingModes(t *testing.T) {
	s := NewVehicle(nil).NewSession()
	for _, command := range []string{"ATE0", "ATSP6", "ATH1", "ATS0"} {
		if response, _ := s.handleAT(command); !strings.Contains(response, "OK") {
			t.Fatalf("%s response = %q", command, response)
		}
	}
	response, _ := s.handleOBD("010C")
	if !strings.HasPrefix(response, "7E804410C") {
		t.Fatalf("header/compact response = %q", response)
	}

	s.handleAT("ATCAF0")
	response, _ = s.handleOBD("010D")
	if !strings.HasPrefix(response, "7E803410D") {
		t.Fatalf("CAF0 response = %q", response)
	}
}

func TestTransmitHeaderSelectsResponseAddress(t *testing.T) {
	s := NewVehicle(nil).NewSession()
	s.echo, s.auto, s.headers = false, false, true
	s.handleAT("ATSH7E2")
	response, _ := s.handleOBD("010D")
	if !strings.HasPrefix(response, "7EA ") {
		t.Fatalf("11-bit response header = %q", response)
	}
	s.handleAT("ATSH18DA10F1")
	response, _ = s.handleOBD("010D")
	if !strings.HasPrefix(response, "18DAF110 ") {
		t.Fatalf("29-bit response header = %q", response)
	}
}

func TestProfilesLoadAndDriveResponses(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("profiles", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("profile glob: %v, paths=%v", err, paths)
	}
	for _, path := range paths {
		profile, err := LoadProfile(path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		s := NewVehicle(profile).NewSession()
		s.echo = false
		s.auto = false
		response, handled := s.handleOBD("010D")
		if !handled || !strings.HasPrefix(response, "41 0D ") {
			t.Fatalf("profile %s speed response = %q", path, response)
		}
	}
}

// connect returns a client talking to v over a real socket.
func connect(t *testing.T, v *Vehicle) *elm327.Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go v.Serve(listener, nil)
	client := &elm327.Client{SyncTimeout: time.Second, ReadTimeout: 2 * time.Second}
	t.Cleanup(func() { client.Close() })
	if err := client.Connect(listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClientReadsEveryService(t *testing.T) {
	v := NewVehicle(nil)
	client := connect(t, v)
	if err := client.Initialize(); err != nil {
		t.Fatal(err)
	}

	pids, err := client.SupportedPIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range drivePIDs {
		if !slices.Contains(pids, want) {
			t.Errorf("PID %02X not advertised", want)
		}
	}
	for _, pid := range drivePIDs {
		reading, err := client.ReadPID(pid)
		if err != nil {
			t.Errorf("PID %02X: %v", pid, err)
			continue
		}
		if _, known := obd.Lookup(pid); !known {
			t.Errorf("PID %02X has no definition", pid)
		}
		if reading.Value == nil && reading.Text == "" {
			t.Errorf("PID %02X decoded to nothing: %+v", pid, reading)
		}
	}

	info, err := client.AdapterInfo()
	if err != nil || info.Version != "ELM327 v1.5" || !strings.Contains(info.Protocol, "CAN") || info.Voltage == nil {
		t.Errorf("adapter = %+v, %v", info, err)
	}

	vehicle, err := client.VehicleInfo()
	if err != nil {
		t.Fatal(err)
	}
	if vehicle.VIN != defaultVIN || vehicle.ECUName != "ECM-EngineControl" ||
		!slices.Equal(vehicle.CalibrationIDs, []string{"SIM-CAL-00000001"}) ||
		!slices.Equal(vehicle.CVNs, []string{"12AB34CD"}) ||
		!slices.Equal(vehicle.ECUs, []string{"7E8"}) {
		t.Errorf("vehicle = %+v", vehicle)
	}

	status, err := client.MonitorStatus()
	if err != nil || !status.MIL || status.DTCCount != 2 || status.Ready() {
		t.Errorf("monitor status = %+v, %v", status, err)
	}
	stored, err := client.ReadDTCs(obd.DTCStored)
	if err != nil || len(stored) != 2 || stored[0].Code != "P0301" || stored[1].Code != "P0420" || stored[0].ECU != "7E8" {
		t.Errorf("stored = %+v, %v", stored, err)
	}
	if stored[0].Description != "Cylinder 1 misfire detected" {
		t.Errorf("description = %q", stored[0].Description)
	}
	pending, _ := client.ReadDTCs(obd.DTCPending)
	if len(pending) != 1 || pending[0].Code != "P0171" {
		t.Errorf("pending = %+v", pending)
	}

	frame, err := client.FreezeFrame()
	if err != nil || frame == nil || frame.DTC.Code != "P0301" {
		t.Fatalf("freeze frame = %+v, %v", frame, err)
	}
	keys := map[string]bool{}
	for _, r := range frame.Readings {
		keys[r.Key] = true
	}
	if !keys["engine_rpm"] || !keys["coolant_temp"] || keys["freeze_dtc"] {
		t.Errorf("freeze frame readings = %v", keys)
	}

	if err := client.ClearDTCs(); err != nil {
		t.Fatal(err)
	}
	stored, _ = client.ReadDTCs(obd.DTCStored)
	permanent, _ := client.ReadDTCs(obd.DTCPermanent)
	if len(stored) != 0 || len(permanent) != 1 {
		t.Errorf("after clear stored=%+v permanent=%+v", stored, permanent)
	}
	if frame, err := client.FreezeFrame(); err != nil || frame != nil {
		t.Errorf("freeze frame after clear = %+v, %v", frame, err)
	}
	status, _ = client.MonitorStatus()
	if status.MIL || status.DTCCount != 0 || status.Ready() {
		t.Errorf("monitor status after clear = %+v", status)
	}
}

// The client must not depend on its own settings: an adapter left in any
// formatting mode still reads back the same values.
func TestClientParsesEveryFormatting(t *testing.T) {
	setups := [][]string{
		{"ATE1", "ATH0", "ATS1"},
		{"ATE0", "ATH0", "ATS0"},
		{"ATE0", "ATH1", "ATS0"},
		{"ATE0", "ATH1", "ATS1", "ATCAF0"},
		{"ATE0", "ATH0", "ATS1", "ATCAF0"},
		{"ATE0", "ATH1", "ATS1", "ATSH18DA10F1"},
		{"ATE0", "ATH1", "ATS0", "ATSH18DA10F1"},
		{"ATE0", "ATL1", "ATH1", "ATS1"},
	}
	for _, setup := range setups {
		t.Run(strings.Join(setup, " "), func(t *testing.T) {
			client := connect(t, NewVehicle(nil))
			for _, command := range setup {
				if _, err := client.Call(command); err != nil {
					t.Fatal(err)
				}
			}
			reading, err := client.ReadPID(0x33)
			if err != nil || reading.Value == nil || *reading.Value != 101 {
				t.Fatalf("barometric = %+v, %v", reading, err)
			}
			info, err := client.VehicleInfo()
			if err != nil || info.VIN != defaultVIN {
				t.Fatalf("VIN = %q, %v", info.VIN, err)
			}
			stored, err := client.ReadDTCs(obd.DTCStored)
			if err != nil || len(stored) != 2 {
				t.Fatalf("stored = %+v, %v", stored, err)
			}
		})
	}
}

func TestEncodeValueRoundTrip(t *testing.T) {
	values := map[byte]float64{
		0x04: 50, 0x05: 90, 0x06: -10, 0x0A: 300, 0x0B: 40, 0x0C: 2500, 0x0D: 88, 0x0E: 12.5, 0x0F: 25, 0x10: 12.34,
		0x11: 40, 0x1F: 600, 0x2F: 62, 0x31: 1234, 0x3C: 650, 0x42: 13.9, 0x43: 60, 0x44: 1, 0x46: 22, 0x5C: 95,
		0x5E: 3.5, 0x61: 40, 0xA6: 48210.5,
	}
	for pid, want := range values {
		data := encodeValue(pid, want)
		if data == nil {
			t.Errorf("PID %02X has no encoder", pid)
			continue
		}
		reading, err := obd.Decode(pid, data)
		if err != nil {
			t.Errorf("PID %02X: %v", pid, err)
			continue
		}
		// One-byte percent scales only resolve about half a percent.
		if math.Abs(*reading.Value-want) > 0.6 {
			t.Errorf("PID %02X = %v, want %v", pid, *reading.Value, want)
		}
	}
}

func TestDriveCycleStaysInRange(t *testing.T) {
	v := NewVehicle(nil)
	for i := 0; i < 3600; i++ {
		v.now = func() time.Time { return v.start.Add(time.Duration(i) * time.Second) }
		for _, pid := range drivePIDs {
			data := v.driveData(pid, v.state(v.now()))
			if data == nil {
				t.Fatalf("PID %02X at %ds has no data", pid, i)
			}
			r, err := obd.Decode(pid, data)
			if err != nil {
				t.Fatalf("PID %02X at %ds: %v", pid, i, err)
			}
			d, _ := obd.Lookup(pid)
			if r.Value != nil && d.Min != d.Max && (*r.Value < d.Min || *r.Value > d.Max) {
				t.Fatalf("PID %02X at %ds = %v outside %v..%v", pid, i, *r.Value, d.Min, d.Max)
			}
		}
	}
}
