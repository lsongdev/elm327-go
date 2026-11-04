package obd

import (
	"errors"
	"math"
	"slices"
	"testing"
)

func TestDecodeScalars(t *testing.T) {
	tests := []struct {
		pid  byte
		data []byte
		want float64
		unit string
	}{
		{0x04, []byte{0xFF}, 100, "%"},
		{0x05, []byte{0x7B}, 83, "°C"},
		{0x06, []byte{0x80}, 0, "%"},
		{0x07, []byte{0x00}, -100, "%"},
		{0x0A, []byte{0x64}, 300, "kPa"},
		{0x0B, []byte{0x21}, 33, "kPa"},
		{0x0C, []byte{0x1A, 0xF8}, 1726, "rpm"},
		{0x0D, []byte{0x3C}, 60, "km/h"},
		{0x0E, []byte{0x90}, 8, "°"},
		{0x10, []byte{0x01, 0xF4}, 5, "g/s"},
		{0x1F, []byte{0x01, 0x00}, 256, "s"},
		{0x22, []byte{0x03, 0xE8}, 79, "kPa"},
		{0x23, []byte{0x00, 0x64}, 1000, "kPa"},
		{0x32, []byte{0xFF, 0xFC}, -1, "Pa"},
		{0x3C, []byte{0x13, 0x88}, 460, "°C"},
		{0x42, []byte{0x36, 0xB0}, 14, "V"},
		{0x44, []byte{0x80, 0x00}, 1, "λ"},
		{0x53, []byte{0x00, 0xC8}, 1, "kPa"},
		{0x54, []byte{0xFF, 0xFF}, -1, "Pa"},
		{0x5C, []byte{0x82}, 90, "°C"},
		{0x5D, []byte{0x69, 0x00}, 0, "°"},
		{0x5E, []byte{0x00, 0x64}, 5, "L/h"},
		{0x61, []byte{0x7D}, 0, "%"},
		{0xA6, []byte{0x00, 0x01, 0x86, 0xA0}, 10000, "km"},
	}
	for _, tt := range tests {
		r, err := Decode(tt.pid, tt.data)
		if err != nil {
			t.Fatalf("pid %02X: %v", tt.pid, err)
		}
		if r.Value == nil || math.Abs(*r.Value-tt.want) > 1e-9 {
			t.Errorf("pid %02X = %v, want %v", tt.pid, r.Value, tt.want)
		}
		if r.Unit != tt.unit {
			t.Errorf("pid %02X unit = %q, want %q", tt.pid, r.Unit, tt.unit)
		}
	}
}

func TestDecodeMultiValue(t *testing.T) {
	r, err := Decode(0x14, []byte{0xC8, 0x80})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Fields) != 2 || *r.Fields[0].Value != 1 || *r.Fields[1].Value != 0 {
		t.Errorf("O2 narrow = %+v", r.Fields)
	}
	// FF in the trim byte means the sensor is not used for trim.
	if r, _ = Decode(0x15, []byte{0x64, 0xFF}); len(r.Fields) != 1 {
		t.Errorf("unused trim fields = %+v", r.Fields)
	}
	r, _ = Decode(0x34, []byte{0x80, 0x00, 0x80, 0x00})
	if *r.Fields[0].Value != 1 || *r.Fields[1].Value != 0 {
		t.Errorf("O2 wide current = %+v", r.Fields)
	}
	r, _ = Decode(0x67, []byte{0x01, 0x7B, 0x00})
	if len(r.Fields) != 1 || *r.Fields[0].Value != 83 {
		t.Errorf("coolant sensors = %+v", r.Fields)
	}
}

func TestDecodeEnums(t *testing.T) {
	tests := []struct {
		pid  byte
		data []byte
		want string
	}{
		{0x03, []byte{0x02, 0x00}, "Closed loop"},
		{0x12, []byte{0x04}, "Atmosphere or off"},
		{0x13, []byte{0x03}, "B1S1 B1S2"},
		{0x1C, []byte{0x06}, "EOBD (Europe)"},
		{0x1D, []byte{0x04}, "B2S1"},
		{0x51, []byte{0x01}, "Gasoline"},
		{0x51, []byte{0xEE}, "Unknown (238)"},
		{0x02, []byte{0x03, 0x01}, "P0301"},
		{0x02, []byte{0x00, 0x00}, "None"},
	}
	for _, tt := range tests {
		r, err := Decode(tt.pid, tt.data)
		if err != nil {
			t.Fatal(err)
		}
		if r.Text != tt.want {
			t.Errorf("pid %02X % X = %q, want %q", tt.pid, tt.data, r.Text, tt.want)
		}
	}
}

func TestDecodeShortAndUnknown(t *testing.T) {
	if _, err := Decode(0x0C, []byte{0x1A}); !errors.Is(err, ErrShortData) {
		t.Errorf("short payload error = %v", err)
	}
	r, err := Decode(0xFE, []byte{0xAB, 0xCD})
	if err != nil || r.Raw != "ABCD" || r.Value != nil {
		t.Errorf("unknown pid = %+v, %v", r, err)
	}
	// CAN padding after the defined size does not change the value.
	r, err = Decode(0x0D, []byte{0x3C, 0xAA, 0xAA})
	if err != nil || *r.Value != 60 || r.Raw != "3C" {
		t.Errorf("padded = %+v, %v", r, err)
	}
}

func TestEveryDefinitionDecodes(t *testing.T) {
	for _, d := range Definitions() {
		if d.Key == "" || d.Name == "" || d.Size == 0 {
			t.Errorf("pid %02X incomplete: %+v", d.PID, d)
		}
		if IsBitmap(d.PID) {
			t.Errorf("pid %02X is a bitmap and must not be polled as a value", d.PID)
		}
		for _, fill := range []byte{0x00, 0x7F, 0xFF} {
			data := make([]byte, d.Size)
			for i := range data {
				data[i] = fill
			}
			if _, err := Decode(d.PID, data); err != nil {
				t.Errorf("pid %02X: %v", d.PID, err)
			}
		}
		if _, err := Decode(d.PID, make([]byte, d.Size-1)); err == nil {
			t.Errorf("pid %02X accepted a short payload", d.PID)
		}
	}
}

func TestSupportedBitmap(t *testing.T) {
	// The bitmap of a real adapter capture, 7E8 06 41 00 BE 3F A8 13.
	got, err := ParseSupported(0x00, []byte{0xBE, 0x3F, 0xA8, 0x13})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x01, 0x03, 0x04, 0x05, 0x06, 0x07, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x13, 0x15, 0x1C, 0x1F, 0x20}
	if !slices.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}
	if encoded := EncodeSupported(0x00, want); !slices.Equal(encoded, []byte{0xBE, 0x3F, 0xA8, 0x13}) {
		t.Fatalf("encoded % X", encoded)
	}
	if _, err := ParseSupported(0, []byte{1}); err == nil {
		t.Fatal("expected short bitmap error")
	}
}

func TestMonitorStatus(t *testing.T) {
	// MIL on, 2 DTCs; spark; misfire+fuel available, fuel incomplete; catalyst and
	// EGR available, EGR incomplete.
	s, err := ParseMonitorStatus([]byte{0x82, 0x23, 0x81, 0x80})
	if err != nil {
		t.Fatal(err)
	}
	if !s.MIL || s.DTCCount != 2 || s.Ignition != "spark" || s.Ready() {
		t.Fatalf("status = %+v", s)
	}
	byKey := map[string]Monitor{}
	for _, m := range s.Monitors {
		byKey[m.Key] = m
	}
	if m := byKey["misfire"]; !m.Available || !m.Complete {
		t.Errorf("misfire = %+v", m)
	}
	if m := byKey["fuel_system"]; !m.Available || m.Complete {
		t.Errorf("fuel = %+v", m)
	}
	if m := byKey["catalyst"]; !m.Available || !m.Complete {
		t.Errorf("catalyst = %+v", m)
	}
	if m := byKey["egr"]; !m.Available || m.Complete {
		t.Errorf("egr = %+v", m)
	}
	if m := byKey["evap"]; m.Available {
		t.Errorf("evap = %+v", m)
	}
	diesel, _ := ParseMonitorStatus([]byte{0x00, 0x08, 0x00, 0x00})
	if diesel.Ignition != "compression" || diesel.MIL {
		t.Errorf("diesel = %+v", diesel)
	}
	if !diesel.Ready() {
		t.Error("nothing available should count as ready")
	}
}

func TestDTCCodes(t *testing.T) {
	tests := map[string][2]byte{
		"P0301": {0x03, 0x01},
		"C0035": {0x40, 0x35},
		"B1234": {0x92, 0x34},
		"U0100": {0xC1, 0x00},
		"P2A0F": {0x2A, 0x0F},
	}
	for code, b := range tests {
		if got := DecodeDTC(b[0], b[1]); got != code {
			t.Errorf("DecodeDTC(% X) = %s, want %s", b, got, code)
		}
		encoded, err := EncodeDTC(code)
		if err != nil || encoded[0] != b[0] || encoded[1] != b[1] {
			t.Errorf("EncodeDTC(%s) = % X, %v", code, encoded, err)
		}
	}
	for _, bad := range []string{"", "P030", "X0301", "P4301", "P03G1"} {
		if _, err := EncodeDTC(bad); err == nil {
			t.Errorf("EncodeDTC(%q) accepted", bad)
		}
	}
}

func TestParseDTCs(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    []string
	}{
		{"can with count", []byte{0x43, 0x02, 0x03, 0x01, 0x04, 0x20}, []string{"P0301", "P0420"}},
		{"can none", []byte{0x43, 0x00}, []string{}},
		{"legacy padded", []byte{0x43, 0x03, 0x01, 0x04, 0x20, 0x00, 0x00}, []string{"P0301", "P0420"}},
		{"legacy none", []byte{0x43, 0, 0, 0, 0, 0, 0}, []string{}},
		{"legacy two frames", []byte{0x43, 0x03, 0x01, 0x03, 0x02, 0x03, 0x03, 0x03, 0x04, 0, 0, 0, 0}, []string{"P0301", "P0302", "P0303", "P0304"}},
		{"count limits padding", []byte{0x47, 0x01, 0x01, 0x71, 0xAA, 0xAA}, []string{"P0171"}},
		{"truncated", []byte{0x43}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseDTCs(tt.payload); !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewDTC(t *testing.T) {
	d := NewDTC("P0302")
	if d.System != "Powertrain" || !d.Generic || d.Description != "Cylinder 2 misfire detected" {
		t.Errorf("P0302 = %+v", d)
	}
	if d = NewDTC("P1234"); d.Generic || d.Description != "" {
		t.Errorf("P1234 = %+v", d)
	}
	if d = NewDTC("U0100"); d.System != "Network" {
		t.Errorf("U0100 = %+v", d)
	}
}

func TestVehicleInfo(t *testing.T) {
	const vin = "1G1JC5444R7252367"
	if got := ParseVIN(append([]byte{0x01}, vin...)); got != vin {
		t.Errorf("CAN VIN = %q", got)
	}
	if got := ParseVIN(append([]byte{0x01, 0, 0, 0}, vin...)); got != vin {
		t.Errorf("legacy VIN = %q", got)
	}
	cal := append([]byte{0x02}, "CAL1234567\x00\x00\x00\x00\x00\x00CAL7654321\x00\x00\x00\x00\x00\x00"...)
	if got := ParseCalibrationIDs(cal); !slices.Equal(got, []string{"CAL1234567", "CAL7654321"}) {
		t.Errorf("calibration IDs = %q", got)
	}
	if got := ParseCVNs([]byte{0x01, 0x12, 0xAB, 0x34, 0xCD}); !slices.Equal(got, []string{"12AB34CD"}) {
		t.Errorf("CVNs = %q", got)
	}
	if got := ParseCVNs([]byte{0x12, 0xAB, 0x34, 0xCD}); !slices.Equal(got, []string{"12AB34CD"}) {
		t.Errorf("CVNs without count = %q", got)
	}
	if got := ParseECUName(append([]byte{0x01}, "ECM\x00-EngineControl\x00\x00\x00\x00"...)); got != "ECM-EngineControl" {
		t.Errorf("ECU name = %q", got)
	}
}
