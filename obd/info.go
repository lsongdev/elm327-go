package obd

import (
	"encoding/hex"
	"strings"
)

// Mode 09 information types.
const (
	InfoSupported     byte = 0x00
	InfoVIN           byte = 0x02
	InfoCalibrationID byte = 0x04
	InfoCVN           byte = 0x06
	InfoPerformance   byte = 0x08
	InfoECUName       byte = 0x0A
)

// VehicleInfo is the Mode 09 identification of a vehicle.
type VehicleInfo struct {
	VIN            string   `json:"vin,omitempty"`
	CalibrationIDs []string `json:"calibration_ids,omitempty"`
	CVNs           []string `json:"cvns,omitempty"`
	ECUName        string   `json:"ecu_name,omitempty"`
	ECUs           []string `json:"ecus,omitempty"` // addresses that answered
}

// items strips the optional count byte of a Mode 09 payload (after "49 <type>")
// and splits the rest into width-byte items. The count is not always sent, so
// the length decides: one extra byte means a count is present.
func items(data []byte, width int) [][]byte {
	switch {
	case len(data)%width == 1:
		data = data[1:]
	case len(data)%width != 0 || len(data) == 0:
		return nil
	}
	var out [][]byte
	for len(data) >= width {
		out = append(out, data[:width])
		data = data[width:]
	}
	return out
}

func text(b []byte) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7E {
			return -1
		}
		return r
	}, string(b)))
}

// ParseVIN decodes the data of infotype 02 (after "49 02"). Legacy protocols
// pad the 17 characters with leading zeros.
func ParseVIN(data []byte) string {
	vin := text(data)
	if len(vin) > 17 {
		vin = vin[len(vin)-17:]
	}
	return vin
}

// ParseCalibrationIDs decodes the data of infotype 04: 16-character IDs.
func ParseCalibrationIDs(data []byte) []string {
	var out []string
	for _, item := range items(data, 16) {
		if id := text(item); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// ParseCVNs decodes the data of infotype 06: 4-byte checksums in hex.
func ParseCVNs(data []byte) []string {
	var out []string
	for _, item := range items(data, 4) {
		out = append(out, strings.ToUpper(hex.EncodeToString(item)))
	}
	return out
}

// ParseECUName decodes the data of infotype 0A: a 20-character name.
func ParseECUName(data []byte) string {
	if parts := items(data, 20); len(parts) > 0 {
		return text(parts[0])
	}
	return text(data)
}
