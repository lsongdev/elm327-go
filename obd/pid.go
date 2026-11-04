// Package obd decodes OBD-II (SAE J1979 / ISO 15031-5) service payloads. It
// knows nothing about the adapter or transport: every function takes the bytes
// an ECU sent, so it can be used with any ELM327 or CAN source.
package obd

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrShortData reports a payload with fewer bytes than the PID defines.
var ErrShortData = errors.New("obd: payload too short")

// Group classifies PIDs for display.
type Group string

const (
	GroupEngine      Group = "engine"
	GroupFuel        Group = "fuel"
	GroupAir         Group = "air"
	GroupTemperature Group = "temperature"
	GroupEmissions   Group = "emissions"
	GroupOxygen      Group = "oxygen"
	GroupElectrical  Group = "electrical"
	GroupDriver      Group = "driver"
	GroupStatus      Group = "status"
)

// Field is one named part of a PID that reports several values.
type Field struct {
	Key   string   `json:"key"`
	Name  string   `json:"name"`
	Unit  string   `json:"unit,omitempty"`
	Value *float64 `json:"value,omitempty"`
	Text  string   `json:"text,omitempty"`
}

// Reading is one decoded PID value. Value holds the primary number and Text an
// enumerated state; a PID has either or both. Fields lists every value of a PID
// that reports more than one.
type Reading struct {
	PID    string   `json:"pid"` // two hex digits, "0C"
	Key    string   `json:"key"`
	Name   string   `json:"name"`
	Unit   string   `json:"unit,omitempty"`
	Group  Group    `json:"group,omitempty"`
	Value  *float64 `json:"value,omitempty"`
	Text   string   `json:"text,omitempty"`
	Fields []Field  `json:"fields,omitempty"`
	Raw    string   `json:"raw"` // the data bytes in hex
}

// Definition describes a Mode 01 PID. The same definitions decode Mode 02
// freeze frame data, which has the same layout.
type Definition struct {
	PID   byte    `json:"-"`
	Key   string  `json:"key"`
	Name  string  `json:"name"`
	Unit  string  `json:"unit,omitempty"`
	Group Group   `json:"group"`
	Size  int     `json:"size"`
	Min   float64 `json:"min"` // display range; Min == Max means no range
	Max   float64 `json:"max"`

	decode func(b []byte, r *Reading)
}

// IsBitmap reports whether pid is a "supported PIDs" range query (00, 20, ...).
func IsBitmap(pid byte) bool { return pid%0x20 == 0 }

// Lookup returns the definition of a Mode 01 PID.
func Lookup(pid byte) (Definition, bool) {
	d, ok := definitions[pid]
	return d, ok
}

// Definitions returns every known PID in numeric order.
func Definitions() []Definition {
	out := make([]Definition, 0, len(definitions))
	for _, d := range definitions {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Definition) int { return int(a.PID) - int(b.PID) })
	return out
}

// Decode decodes the data bytes of a Mode 01 PID, excluding the "41 <pid>"
// prefix. Bytes beyond the defined size (CAN padding) are ignored. A PID with
// no definition returns a Reading with only Raw set.
func Decode(pid byte, data []byte) (Reading, error) {
	d, ok := definitions[pid]
	if !ok {
		return Reading{
			PID:  fmt.Sprintf("%02X", pid),
			Key:  fmt.Sprintf("pid_%02x", pid),
			Name: fmt.Sprintf("PID %02X", pid),
			Raw:  strings.ToUpper(hex.EncodeToString(data)),
		}, nil
	}
	if len(data) < d.Size {
		return Reading{}, fmt.Errorf("%w: pid %02X needs %d bytes, got %d", ErrShortData, pid, d.Size, len(data))
	}
	data = data[:d.Size]
	r := Reading{
		PID:   fmt.Sprintf("%02X", pid),
		Key:   d.Key,
		Name:  d.Name,
		Unit:  d.Unit,
		Group: d.Group,
		Raw:   strings.ToUpper(hex.EncodeToString(data)),
	}
	d.decode(data, &r)
	return r, nil
}

// ParseSupported returns the PIDs a 4-byte range bitmap marks as supported.
// base is the queried PID (0x00, 0x20, ...); bit 7 of the first byte is base+1.
func ParseSupported(base byte, data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: supported bitmap needs 4 bytes, got %d", ErrShortData, len(data))
	}
	var out []byte
	for i := range 32 {
		if data[i/8]&(0x80>>(i%8)) != 0 {
			pid := int(base) + i + 1
			if pid <= 0xFF {
				out = append(out, byte(pid))
			}
		}
	}
	return out, nil
}

// EncodeSupported builds the 4-byte range bitmap for base from a list of PIDs.
func EncodeSupported(base byte, pids []byte) []byte {
	out := make([]byte, 4)
	for _, pid := range pids {
		offset := int(pid) - int(base) - 1
		if offset >= 0 && offset < 32 {
			out[offset/8] |= 0x80 >> (offset % 8)
		}
	}
	return out
}

func word(b []byte, i int) float64 { return float64(uint16(b[i])<<8 | uint16(b[i+1])) }

func signedWord(b []byte, i int) float64 { return float64(int16(uint16(b[i])<<8 | uint16(b[i+1]))) }

func percent(a byte) float64 { return float64(a) * 100 / 255 }

// trim is the signed percent scale of fuel trims: 0 is -100%, 128 is 0%.
func trim(a byte) float64 { return float64(a)*100/128 - 100 }

func temperature(a byte) float64 { return float64(a) - 40 }

// torque is the percent scale of engine torque: A-125.
func torque(a byte) float64 { return float64(a) - 125 }

func ptr(v float64) *float64 { return &v }

func num(key, name, unit string, v float64) Field {
	return Field{Key: key, Name: name, Unit: unit, Value: ptr(v)}
}

// scalar decodes a PID with one number.
func scalar(fn func(b []byte) float64) func([]byte, *Reading) {
	return func(b []byte, r *Reading) { r.Value = ptr(fn(b)) }
}

// fields decodes a PID with several numbers; the first is the primary value.
func fields(fn func(b []byte) []Field) func([]byte, *Reading) {
	return func(b []byte, r *Reading) {
		r.Fields = fn(b)
		if len(r.Fields) > 0 {
			r.Value = r.Fields[0].Value
		}
	}
}

// enum decodes a one-byte code through a table.
func enum(table map[byte]string) func([]byte, *Reading) {
	return func(b []byte, r *Reading) {
		r.Value = ptr(float64(b[0]))
		if text, ok := table[b[0]]; ok {
			r.Text = text
		} else {
			r.Text = fmt.Sprintf("Unknown (%d)", b[0])
		}
	}
}

func a(b []byte) float64 { return float64(b[0]) }

func ab(b []byte) float64 { return word(b, 0) }

func pctA(b []byte) float64 { return percent(b[0]) }

func tempA(b []byte) float64 { return temperature(b[0]) }

func trimA(b []byte) float64 { return trim(b[0]) }

func torqueA(b []byte) float64 { return torque(b[0]) }

var definitions = buildDefinitions()

func buildDefinitions() map[byte]Definition {
	list := []Definition{
		// Status
		{PID: 0x01, Key: "monitor_status", Name: "Monitor status since DTCs cleared", Group: GroupStatus, Size: 4, decode: decodeMonitorStatus},
		{PID: 0x02, Key: "freeze_dtc", Name: "DTC that caused the freeze frame", Group: GroupStatus, Size: 2, decode: decodeFreezeDTC},
		{PID: 0x1C, Key: "obd_standard", Name: "OBD standard", Group: GroupStatus, Size: 1, decode: enum(obdStandards)},
		{PID: 0x1E, Key: "auxiliary_input", Name: "Auxiliary input status", Group: GroupStatus, Size: 1, decode: decodeAuxInput},
		{PID: 0x1F, Key: "run_time", Name: "Run time since engine start", Unit: "s", Group: GroupStatus, Size: 2, decode: scalar(ab)},
		{PID: 0x21, Key: "distance_with_mil", Name: "Distance with MIL on", Unit: "km", Group: GroupStatus, Size: 2, decode: scalar(ab)},
		{PID: 0x30, Key: "warmups_since_clear", Name: "Warm-ups since DTCs cleared", Group: GroupStatus, Size: 1, decode: scalar(a)},
		{PID: 0x31, Key: "distance_since_clear", Name: "Distance since DTCs cleared", Unit: "km", Group: GroupStatus, Size: 2, decode: scalar(ab)},
		{PID: 0x41, Key: "monitor_status_cycle", Name: "Monitor status this drive cycle", Group: GroupStatus, Size: 4, decode: decodeMonitorCycle},
		{PID: 0x4D, Key: "time_with_mil", Name: "Time with MIL on", Unit: "min", Group: GroupStatus, Size: 2, decode: scalar(ab)},
		{PID: 0x4E, Key: "time_since_clear", Name: "Time since DTCs cleared", Unit: "min", Group: GroupStatus, Size: 2, decode: scalar(ab)},
		{PID: 0x5F, Key: "emission_requirements", Name: "Emission requirements", Group: GroupStatus, Size: 1, decode: scalar(a)},
		{PID: 0xA6, Key: "odometer", Name: "Odometer", Unit: "km", Group: GroupStatus, Size: 4, decode: scalar(func(b []byte) float64 {
			return float64(uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3])) / 10
		})},

		// Engine
		{PID: 0x04, Key: "engine_load", Name: "Calculated engine load", Unit: "%", Group: GroupEngine, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x0C, Key: "engine_rpm", Name: "Engine speed", Unit: "rpm", Group: GroupEngine, Size: 2, Max: 8000, decode: scalar(func(b []byte) float64 { return word(b, 0) / 4 })},
		{PID: 0x0D, Key: "vehicle_speed", Name: "Vehicle speed", Unit: "km/h", Group: GroupEngine, Size: 1, Max: 240, decode: scalar(a)},
		{PID: 0x0E, Key: "timing_advance", Name: "Timing advance", Unit: "°", Group: GroupEngine, Size: 1, Min: -64, Max: 64, decode: scalar(func(b []byte) float64 { return float64(b[0])/2 - 64 })},
		{PID: 0x43, Key: "absolute_load", Name: "Absolute load", Unit: "%", Group: GroupEngine, Size: 2, Max: 200, decode: scalar(func(b []byte) float64 { return word(b, 0) * 100 / 255 })},
		{PID: 0x61, Key: "demand_torque", Name: "Driver demand torque", Unit: "%", Group: GroupEngine, Size: 1, Min: -125, Max: 130, decode: scalar(torqueA)},
		{PID: 0x62, Key: "actual_torque", Name: "Actual engine torque", Unit: "%", Group: GroupEngine, Size: 1, Min: -125, Max: 130, decode: scalar(torqueA)},
		{PID: 0x63, Key: "reference_torque", Name: "Engine reference torque", Unit: "N·m", Group: GroupEngine, Size: 2, decode: scalar(ab)},
		{PID: 0x64, Key: "torque_data", Name: "Engine torque data", Unit: "%", Group: GroupEngine, Size: 5, decode: fields(func(b []byte) []Field {
			return []Field{
				num("idle", "Idle", "%", torque(b[0])),
				num("point_1", "Point 1", "%", torque(b[1])),
				num("point_2", "Point 2", "%", torque(b[2])),
				num("point_3", "Point 3", "%", torque(b[3])),
				num("point_4", "Point 4", "%", torque(b[4])),
			}
		})},
		{PID: 0x8E, Key: "friction_torque", Name: "Engine friction torque", Unit: "%", Group: GroupEngine, Size: 1, Min: -125, Max: 130, decode: scalar(torqueA)},
		{PID: 0xA4, Key: "transmission_gear", Name: "Transmission gear ratio", Group: GroupEngine, Size: 4, decode: scalar(func(b []byte) float64 { return word(b, 2) / 1000 })},

		// Fuel
		{PID: 0x03, Key: "fuel_system_status", Name: "Fuel system status", Group: GroupFuel, Size: 2, decode: decodeFuelSystem},
		{PID: 0x06, Key: "short_fuel_trim_1", Name: "Short term fuel trim, bank 1", Unit: "%", Group: GroupFuel, Size: 1, Min: -100, Max: 99.2, decode: scalar(trimA)},
		{PID: 0x07, Key: "long_fuel_trim_1", Name: "Long term fuel trim, bank 1", Unit: "%", Group: GroupFuel, Size: 1, Min: -100, Max: 99.2, decode: scalar(trimA)},
		{PID: 0x08, Key: "short_fuel_trim_2", Name: "Short term fuel trim, bank 2", Unit: "%", Group: GroupFuel, Size: 1, Min: -100, Max: 99.2, decode: scalar(trimA)},
		{PID: 0x09, Key: "long_fuel_trim_2", Name: "Long term fuel trim, bank 2", Unit: "%", Group: GroupFuel, Size: 1, Min: -100, Max: 99.2, decode: scalar(trimA)},
		{PID: 0x0A, Key: "fuel_pressure", Name: "Fuel pressure", Unit: "kPa", Group: GroupFuel, Size: 1, Max: 765, decode: scalar(func(b []byte) float64 { return float64(b[0]) * 3 })},
		{PID: 0x22, Key: "fuel_rail_pressure", Name: "Fuel rail pressure (vacuum)", Unit: "kPa", Group: GroupFuel, Size: 2, Max: 5177, decode: scalar(func(b []byte) float64 { return word(b, 0) * 0.079 })},
		{PID: 0x23, Key: "fuel_rail_gauge_pressure", Name: "Fuel rail gauge pressure", Unit: "kPa", Group: GroupFuel, Size: 2, Max: 655350, decode: scalar(func(b []byte) float64 { return word(b, 0) * 10 })},
		{PID: 0x2F, Key: "fuel_level", Name: "Fuel level", Unit: "%", Group: GroupFuel, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x51, Key: "fuel_type", Name: "Fuel type", Group: GroupFuel, Size: 1, decode: enum(fuelTypes)},
		{PID: 0x52, Key: "ethanol_percent", Name: "Ethanol fuel", Unit: "%", Group: GroupFuel, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x59, Key: "fuel_rail_absolute_pressure", Name: "Fuel rail absolute pressure", Unit: "kPa", Group: GroupFuel, Size: 2, Max: 655350, decode: scalar(func(b []byte) float64 { return word(b, 0) * 10 })},
		{PID: 0x5D, Key: "injection_timing", Name: "Fuel injection timing", Unit: "°", Group: GroupFuel, Size: 2, Min: -210, Max: 302, decode: scalar(func(b []byte) float64 { return word(b, 0)/128 - 210 })},
		{PID: 0x5E, Key: "fuel_rate", Name: "Engine fuel rate", Unit: "L/h", Group: GroupFuel, Size: 2, Max: 100, decode: scalar(func(b []byte) float64 { return word(b, 0) / 20 })},
		{PID: 0x9D, Key: "fuel_mass_rate", Name: "Fuel mass rate", Unit: "g/s", Group: GroupFuel, Size: 4, decode: fields(func(b []byte) []Field {
			return []Field{
				num("engine", "Engine", "g/s", word(b, 0)/50),
				num("vehicle", "Vehicle", "g/s", word(b, 2)/50),
			}
		})},
		{PID: 0xA2, Key: "cylinder_fuel_rate", Name: "Cylinder fuel rate", Unit: "mg/stroke", Group: GroupFuel, Size: 2, decode: scalar(func(b []byte) float64 { return word(b, 0) / 32 })},

		// Air
		{PID: 0x0B, Key: "intake_pressure", Name: "Intake manifold pressure", Unit: "kPa", Group: GroupAir, Size: 1, Max: 255, decode: scalar(a)},
		{PID: 0x10, Key: "maf_rate", Name: "Mass air flow", Unit: "g/s", Group: GroupAir, Size: 2, Max: 300, decode: scalar(func(b []byte) float64 { return word(b, 0) / 100 })},
		{PID: 0x12, Key: "secondary_air_status", Name: "Secondary air status", Group: GroupAir, Size: 1, decode: enum(secondaryAir)},
		{PID: 0x33, Key: "barometric_pressure", Name: "Barometric pressure", Unit: "kPa", Group: GroupAir, Size: 1, Max: 255, decode: scalar(a)},
		{PID: 0x4F, Key: "maximum_values", Name: "Maximum sensor values", Group: GroupAir, Size: 4, decode: fields(func(b []byte) []Field {
			return []Field{
				num("lambda", "Equivalence ratio", "λ", float64(b[0])),
				num("voltage", "O2 voltage", "V", float64(b[1])),
				num("current", "O2 current", "mA", float64(b[2])),
				num("intake_pressure", "Intake pressure", "kPa", float64(b[3])*10),
			}
		})},
		{PID: 0x50, Key: "maximum_maf", Name: "Maximum MAF sensor value", Unit: "g/s", Group: GroupAir, Size: 4, decode: scalar(func(b []byte) float64 { return float64(b[0]) * 10 })},
		{PID: 0x66, Key: "maf_sensor", Name: "Mass air flow sensors", Unit: "g/s", Group: GroupAir, Size: 5, decode: fields(func(b []byte) []Field {
			var out []Field
			if b[0]&0x01 != 0 {
				out = append(out, num("sensor_a", "Sensor A", "g/s", word(b, 1)/32))
			}
			if b[0]&0x02 != 0 {
				out = append(out, num("sensor_b", "Sensor B", "g/s", word(b, 3)/32))
			}
			return out
		})},

		// Temperature
		{PID: 0x05, Key: "coolant_temp", Name: "Engine coolant temperature", Unit: "°C", Group: GroupTemperature, Size: 1, Min: -40, Max: 215, decode: scalar(tempA)},
		{PID: 0x0F, Key: "intake_temp", Name: "Intake air temperature", Unit: "°C", Group: GroupTemperature, Size: 1, Min: -40, Max: 215, decode: scalar(tempA)},
		{PID: 0x46, Key: "ambient_temp", Name: "Ambient air temperature", Unit: "°C", Group: GroupTemperature, Size: 1, Min: -40, Max: 215, decode: scalar(tempA)},
		{PID: 0x5C, Key: "oil_temp", Name: "Engine oil temperature", Unit: "°C", Group: GroupTemperature, Size: 1, Min: -40, Max: 210, decode: scalar(tempA)},
		{PID: 0x67, Key: "coolant_sensors", Name: "Engine coolant temperature sensors", Unit: "°C", Group: GroupTemperature, Size: 3, decode: fields(func(b []byte) []Field {
			var out []Field
			if b[0]&0x01 != 0 {
				out = append(out, num("sensor_1", "Sensor 1", "°C", temperature(b[1])))
			}
			if b[0]&0x02 != 0 {
				out = append(out, num("sensor_2", "Sensor 2", "°C", temperature(b[2])))
			}
			return out
		})},
		{PID: 0x68, Key: "intake_sensors", Name: "Intake air temperature sensors", Unit: "°C", Group: GroupTemperature, Size: 3, decode: fields(func(b []byte) []Field {
			var out []Field
			if b[0]&0x01 != 0 {
				out = append(out, num("sensor_1", "Sensor 1", "°C", temperature(b[1])))
			}
			if b[0]&0x02 != 0 {
				out = append(out, num("sensor_2", "Sensor 2", "°C", temperature(b[2])))
			}
			return out
		})},
		{PID: 0x3C, Key: "catalyst_temp_b1s1", Name: "Catalyst temperature, bank 1 sensor 1", Unit: "°C", Group: GroupTemperature, Size: 2, Min: -40, Max: 1000, decode: scalar(catalyst)},
		{PID: 0x3D, Key: "catalyst_temp_b2s1", Name: "Catalyst temperature, bank 2 sensor 1", Unit: "°C", Group: GroupTemperature, Size: 2, Min: -40, Max: 1000, decode: scalar(catalyst)},
		{PID: 0x3E, Key: "catalyst_temp_b1s2", Name: "Catalyst temperature, bank 1 sensor 2", Unit: "°C", Group: GroupTemperature, Size: 2, Min: -40, Max: 1000, decode: scalar(catalyst)},
		{PID: 0x3F, Key: "catalyst_temp_b2s2", Name: "Catalyst temperature, bank 2 sensor 2", Unit: "°C", Group: GroupTemperature, Size: 2, Min: -40, Max: 1000, decode: scalar(catalyst)},
		{PID: 0x7C, Key: "dpf_temp", Name: "Diesel particulate filter temperature", Unit: "°C", Group: GroupTemperature, Size: 2, Min: -40, Max: 1000, decode: scalar(catalyst)},

		// Driver inputs
		{PID: 0x11, Key: "throttle_position", Name: "Throttle position", Unit: "%", Group: GroupDriver, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x45, Key: "relative_throttle", Name: "Relative throttle position", Unit: "%", Group: GroupDriver, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x47, Key: "absolute_throttle_b", Name: "Absolute throttle position B", Unit: "%", Group: GroupDriver, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x48, Key: "absolute_throttle_c", Name: "Absolute throttle position C", Unit: "%", Group: GroupDriver, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x49, Key: "accelerator_d", Name: "Accelerator pedal position D", Unit: "%", Group: GroupDriver, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x4A, Key: "accelerator_e", Name: "Accelerator pedal position E", Unit: "%", Group: GroupDriver, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x4B, Key: "accelerator_f", Name: "Accelerator pedal position F", Unit: "%", Group: GroupDriver, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x4C, Key: "commanded_throttle", Name: "Commanded throttle actuator", Unit: "%", Group: GroupDriver, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x5A, Key: "relative_accelerator", Name: "Relative accelerator pedal position", Unit: "%", Group: GroupDriver, Size: 1, Max: 100, decode: scalar(pctA)},

		// Emissions
		{PID: 0x2C, Key: "commanded_egr", Name: "Commanded EGR", Unit: "%", Group: GroupEmissions, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x2D, Key: "egr_error", Name: "EGR error", Unit: "%", Group: GroupEmissions, Size: 1, Min: -100, Max: 99.2, decode: scalar(trimA)},
		{PID: 0x2E, Key: "commanded_evap_purge", Name: "Commanded evaporative purge", Unit: "%", Group: GroupEmissions, Size: 1, Max: 100, decode: scalar(pctA)},
		{PID: 0x32, Key: "evap_vapor_pressure", Name: "Evaporative system vapor pressure", Unit: "Pa", Group: GroupEmissions, Size: 2, Min: -8192, Max: 8192, decode: scalar(func(b []byte) float64 { return signedWord(b, 0) / 4 })},
		{PID: 0x53, Key: "evap_absolute_pressure", Name: "Absolute evaporative system pressure", Unit: "kPa", Group: GroupEmissions, Size: 2, Max: 327.675, decode: scalar(func(b []byte) float64 { return word(b, 0) / 200 })},
		{PID: 0x54, Key: "evap_pressure", Name: "Evaporative system pressure", Unit: "Pa", Group: GroupEmissions, Size: 2, Min: -32768, Max: 32767, decode: scalar(func(b []byte) float64 { return signedWord(b, 0) })},
		{PID: 0x9E, Key: "exhaust_flow_rate", Name: "Exhaust gas flow rate", Unit: "kg/h", Group: GroupEmissions, Size: 2, decode: scalar(func(b []byte) float64 { return word(b, 0) / 5 })},

		// Electrical
		{PID: 0x42, Key: "control_module_voltage", Name: "Control module voltage", Unit: "V", Group: GroupElectrical, Size: 2, Min: 8, Max: 16, decode: scalar(func(b []byte) float64 { return word(b, 0) / 1000 })},
		{PID: 0x5B, Key: "hybrid_battery_life", Name: "Hybrid battery remaining life", Unit: "%", Group: GroupElectrical, Size: 1, Max: 100, decode: scalar(pctA)},

		// Oxygen sensor layout
		{PID: 0x13, Key: "o2_sensors_present", Name: "O2 sensors present (2 banks)", Group: GroupOxygen, Size: 1, decode: decodeO2Present(4)},
		{PID: 0x1D, Key: "o2_sensors_present_4", Name: "O2 sensors present (4 banks)", Group: GroupOxygen, Size: 1, decode: decodeO2Present(2)},
		{PID: 0x44, Key: "commanded_lambda", Name: "Commanded equivalence ratio", Unit: "λ", Group: GroupOxygen, Size: 2, Max: 2, decode: scalar(lambda)},
	}

	// O2 sensors 1-8 in the three report formats, and secondary trims.
	for i := range 8 {
		n := i + 1
		list = append(list,
			Definition{
				PID: 0x14 + byte(i), Key: fmt.Sprintf("o2_sensor_%d", n), Name: fmt.Sprintf("O2 sensor %d voltage", n),
				Unit: "V", Group: GroupOxygen, Size: 2, Max: 1.275, decode: fields(decodeO2Narrow),
			},
			Definition{
				PID: 0x24 + byte(i), Key: fmt.Sprintf("o2_lambda_voltage_%d", n), Name: fmt.Sprintf("O2 sensor %d equivalence ratio", n),
				Unit: "λ", Group: GroupOxygen, Size: 4, Max: 2, decode: fields(decodeO2Voltage),
			},
			Definition{
				PID: 0x34 + byte(i), Key: fmt.Sprintf("o2_lambda_current_%d", n), Name: fmt.Sprintf("O2 sensor %d equivalence ratio (current)", n),
				Unit: "λ", Group: GroupOxygen, Size: 4, Max: 2, decode: fields(decodeO2Current),
			},
		)
	}
	for i, bank := range []string{"1/3", "2/4"} {
		list = append(list,
			Definition{
				PID: 0x55 + byte(i*2), Key: fmt.Sprintf("secondary_short_trim_%d", i+1), Name: "Secondary O2 short term trim, banks " + bank,
				Unit: "%", Group: GroupOxygen, Size: 2, Min: -100, Max: 99.2, decode: fields(decodeBankTrim),
			},
			Definition{
				PID: 0x56 + byte(i*2), Key: fmt.Sprintf("secondary_long_trim_%d", i+1), Name: "Secondary O2 long term trim, banks " + bank,
				Unit: "%", Group: GroupOxygen, Size: 2, Min: -100, Max: 99.2, decode: fields(decodeBankTrim),
			},
		)
	}

	table := make(map[byte]Definition, len(list))
	for _, d := range list {
		if _, dup := table[d.PID]; dup {
			panic(fmt.Sprintf("obd: duplicate PID %02X", d.PID))
		}
		table[d.PID] = d
	}
	return table
}

func catalyst(b []byte) float64 { return word(b, 0)/10 - 40 }

func lambda(b []byte) float64 { return word(b, 0) * 2 / 65536 }

func decodeO2Narrow(b []byte) []Field {
	out := []Field{num("voltage", "Voltage", "V", float64(b[0])/200)}
	if b[1] != 0xFF { // FF: the sensor is not used for trim
		out = append(out, num("trim", "Short term trim", "%", trim(b[1])))
	}
	return out
}

func decodeO2Voltage(b []byte) []Field {
	return []Field{
		num("lambda", "Equivalence ratio", "λ", lambda(b)),
		num("voltage", "Voltage", "V", word(b, 2)*8/65536),
	}
}

func decodeO2Current(b []byte) []Field {
	return []Field{
		num("lambda", "Equivalence ratio", "λ", lambda(b)),
		num("current", "Current", "mA", word(b, 2)/256-128),
	}
}

func decodeBankTrim(b []byte) []Field {
	return []Field{
		num("bank_a", "Bank 1 or 2", "%", trim(b[0])),
		num("bank_b", "Bank 3 or 4", "%", trim(b[1])),
	}
}

// decodeO2Present lists the sensors a PID 13 (2 banks of 4) or PID 1D (4 banks
// of 2) bitmap reports. Bit 0 is bank 1 sensor 1.
func decodeO2Present(sensorsPerBank int) func([]byte, *Reading) {
	return func(b []byte, r *Reading) {
		var present []string
		for bit := range 8 {
			if b[0]&(1<<bit) != 0 {
				present = append(present, fmt.Sprintf("B%dS%d", bit/sensorsPerBank+1, bit%sensorsPerBank+1))
			}
		}
		r.Value = ptr(float64(len(present)))
		r.Text = strings.Join(present, " ")
	}
}

func decodeAuxInput(b []byte, r *Reading) {
	if b[0]&0x01 != 0 {
		r.Text = "PTO active"
	} else {
		r.Text = "PTO inactive"
	}
	r.Value = ptr(float64(b[0] & 0x01))
}

func decodeFreezeDTC(b []byte, r *Reading) {
	code := DecodeDTC(b[0], b[1])
	if b[0] == 0 && b[1] == 0 {
		r.Text = "None"
		return
	}
	r.Text = code
}

func decodeFuelSystem(b []byte, r *Reading) {
	state := func(code byte) string {
		if code == 0 {
			return ""
		}
		if text, ok := fuelSystemStatus[code]; ok {
			return text
		}
		return fmt.Sprintf("Unknown (%d)", code)
	}
	sys1, sys2 := state(b[0]), state(b[1])
	r.Text = sys1
	if sys1 == "" {
		r.Text = "Not reported"
	}
	r.Value = ptr(float64(b[0]))
	r.Fields = []Field{{Key: "system_1", Name: "System 1", Text: sys1}}
	if sys2 != "" {
		r.Fields = append(r.Fields, Field{Key: "system_2", Name: "System 2", Text: sys2})
	}
}

var fuelSystemStatus = map[byte]string{
	0x01: "Open loop: insufficient engine temperature",
	0x02: "Closed loop",
	0x04: "Open loop: engine load or fuel cut",
	0x08: "Open loop: system failure",
	0x10: "Closed loop: feedback fault",
}

var secondaryAir = map[byte]string{
	0x01: "Upstream of catalyst",
	0x02: "Downstream of catalyst",
	0x04: "Atmosphere or off",
	0x08: "Pump on for diagnostics",
}

var obdStandards = map[byte]string{
	1: "OBD-II (CARB)", 2: "OBD (EPA)", 3: "OBD and OBD-II", 4: "OBD-I", 5: "Not OBD compliant",
	6: "EOBD (Europe)", 7: "EOBD and OBD-II", 8: "EOBD and OBD", 9: "EOBD, OBD and OBD-II",
	10: "JOBD (Japan)", 11: "JOBD and OBD-II", 12: "JOBD and EOBD", 13: "JOBD, EOBD and OBD-II",
	17: "Engine Manufacturer Diagnostics (EMD)", 18: "EMD+", 19: "Heavy Duty OBD (Child/Partial)", 20: "Heavy Duty OBD",
	21: "World Wide Harmonized OBD", 23: "Heavy Duty Euro OBD I", 24: "Heavy Duty Euro OBD I (NOx)",
	25: "Heavy Duty Euro OBD II", 26: "Heavy Duty Euro OBD II (NOx)", 28: "OBDBr-1 (Brazil)", 29: "OBDBr-2 (Brazil)",
	30: "KOBD (Korea)", 31: "IOBD I (India)", 32: "IOBD II (India)", 33: "Heavy Duty Euro OBD VI",
}

var fuelTypes = map[byte]string{
	0: "Not available", 1: "Gasoline", 2: "Methanol", 3: "Ethanol", 4: "Diesel", 5: "LPG", 6: "CNG", 7: "Propane", 8: "Electric",
	9: "Bifuel: gasoline", 10: "Bifuel: methanol", 11: "Bifuel: ethanol", 12: "Bifuel: LPG", 13: "Bifuel: CNG", 14: "Bifuel: propane",
	15: "Bifuel: electricity", 16: "Bifuel: electric and combustion", 17: "Hybrid: gasoline", 18: "Hybrid: ethanol", 19: "Hybrid: diesel",
	20: "Hybrid: electric", 21: "Hybrid: electric and combustion", 22: "Hybrid: regenerative", 23: "Bifuel: diesel",
}
