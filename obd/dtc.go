package obd

import "fmt"

// DTCKind selects a diagnostic trouble code service.
type DTCKind byte

const (
	DTCStored    DTCKind = 0x03 // confirmed codes, Mode 03
	DTCPending   DTCKind = 0x07 // codes from the current or last drive cycle, Mode 07
	DTCPermanent DTCKind = 0x0A // codes a clear cannot erase, Mode 0A
)

// DTC is a diagnostic trouble code.
type DTC struct {
	Code        string `json:"code"`                  // "P0301"
	System      string `json:"system"`                // Powertrain, Chassis, Body, Network
	Generic     bool   `json:"generic"`               // SAE-defined rather than manufacturer-specific
	Description string `json:"description,omitempty"` // empty when the code is not in the table
	ECU         string `json:"ecu,omitempty"`         // the responding ECU, when known
}

// DecodeDTC formats the two bytes of a trouble code: the top two bits of the
// first byte pick the letter and the rest are digits.
func DecodeDTC(hi, lo byte) string {
	return fmt.Sprintf("%c%d%X%X%X", "PCBU"[hi>>6], (hi>>4)&0x3, hi&0xF, lo>>4, lo&0xF)
}

// EncodeDTC is the inverse of DecodeDTC.
func EncodeDTC(code string) ([]byte, error) {
	if len(code) != 5 {
		return nil, fmt.Errorf("invalid DTC %q", code)
	}
	var letter byte
	switch code[0] {
	case 'P':
	case 'C':
		letter = 1
	case 'B':
		letter = 2
	case 'U':
		letter = 3
	default:
		return nil, fmt.Errorf("invalid DTC %q", code)
	}
	var digits [4]byte
	for i := range digits {
		c := code[i+1]
		switch {
		case c >= '0' && c <= '9':
			digits[i] = c - '0'
		case c >= 'A' && c <= 'F':
			digits[i] = c - 'A' + 10
		default:
			return nil, fmt.Errorf("invalid DTC %q", code)
		}
	}
	if digits[0] > 3 {
		return nil, fmt.Errorf("invalid DTC %q", code)
	}
	return []byte{letter<<6 | digits[0]<<4 | digits[1], digits[2]<<4 | digits[3]}, nil
}

// NewDTC describes a code from its text form.
func NewDTC(code string) DTC {
	d := DTC{Code: code, Description: dtcDescriptions[code]}
	if len(code) == 5 {
		switch code[0] {
		case 'P':
			d.System = "Powertrain"
			d.Generic = code[1] == '0' || code[1] == '2' || (code[1] == '3' && code[2] >= '4')
		case 'C':
			d.System = "Chassis"
			d.Generic = code[1] == '0' || code[1] == '3'
		case 'B':
			d.System = "Body"
			d.Generic = code[1] == '0' || code[1] == '3'
		case 'U':
			d.System = "Network"
			d.Generic = code[1] == '0' || code[1] == '3'
		}
	}
	return d
}

// ParseDTCs decodes one ECU's Mode 03/07/0A response, including its service
// byte (43, 47 or 4A). CAN responses carry a count after the service byte and
// legacy ones pad with zero codes, which the total length tells apart: a count
// makes the length even, because everything after it comes in pairs.
func ParseDTCs(payload []byte) []string {
	if len(payload) < 2 {
		return nil
	}
	body := payload[1:]
	limit := -1
	if len(payload)%2 == 0 {
		limit = int(body[0])
		body = body[1:]
	}
	codes := []string{}
	for i := 0; i+1 < len(body); i += 2 {
		if limit >= 0 && len(codes) == limit {
			break
		}
		if body[i] == 0 && body[i+1] == 0 {
			continue // padding
		}
		codes = append(codes, DecodeDTC(body[i], body[i+1]))
	}
	return codes
}

// dtcDescriptions covers the common SAE-defined powertrain and network codes.
// Manufacturer-specific codes need that manufacturer's documentation.
var dtcDescriptions = func() map[string]string {
	m := map[string]string{
		"P0010": "Intake camshaft position actuator circuit, bank 1",
		"P0011": "Intake camshaft timing over-advanced, bank 1",
		"P0012": "Intake camshaft timing over-retarded, bank 1",
		"P0013": "Exhaust camshaft position actuator circuit, bank 1",
		"P0014": "Exhaust camshaft timing over-advanced, bank 1",
		"P0016": "Crankshaft/camshaft position correlation, bank 1 sensor A",
		"P0017": "Crankshaft/camshaft position correlation, bank 1 sensor B",
		"P0030": "O2 sensor heater control circuit, bank 1 sensor 1",
		"P0036": "O2 sensor heater control circuit, bank 1 sensor 2",
		"P0087": "Fuel rail/system pressure too low",
		"P0088": "Fuel rail/system pressure too high",
		"P0100": "Mass air flow circuit",
		"P0101": "Mass air flow circuit range/performance",
		"P0102": "Mass air flow circuit low",
		"P0103": "Mass air flow circuit high",
		"P0105": "Manifold pressure/barometric pressure circuit",
		"P0106": "Manifold pressure/barometric pressure range/performance",
		"P0107": "Manifold pressure/barometric pressure circuit low",
		"P0108": "Manifold pressure/barometric pressure circuit high",
		"P0110": "Intake air temperature circuit",
		"P0112": "Intake air temperature circuit low",
		"P0113": "Intake air temperature circuit high",
		"P0115": "Engine coolant temperature circuit",
		"P0116": "Engine coolant temperature range/performance",
		"P0117": "Engine coolant temperature circuit low",
		"P0118": "Engine coolant temperature circuit high",
		"P0120": "Throttle/pedal position sensor A circuit",
		"P0121": "Throttle/pedal position sensor A range/performance",
		"P0122": "Throttle/pedal position sensor A circuit low",
		"P0123": "Throttle/pedal position sensor A circuit high",
		"P0125": "Insufficient coolant temperature for closed loop fuel control",
		"P0128": "Coolant thermostat below regulating temperature",
		"P0130": "O2 sensor circuit, bank 1 sensor 1",
		"P0131": "O2 sensor circuit low voltage, bank 1 sensor 1",
		"P0132": "O2 sensor circuit high voltage, bank 1 sensor 1",
		"P0133": "O2 sensor slow response, bank 1 sensor 1",
		"P0134": "O2 sensor no activity detected, bank 1 sensor 1",
		"P0135": "O2 sensor heater circuit, bank 1 sensor 1",
		"P0136": "O2 sensor circuit, bank 1 sensor 2",
		"P0137": "O2 sensor circuit low voltage, bank 1 sensor 2",
		"P0138": "O2 sensor circuit high voltage, bank 1 sensor 2",
		"P0140": "O2 sensor no activity detected, bank 1 sensor 2",
		"P0141": "O2 sensor heater circuit, bank 1 sensor 2",
		"P0150": "O2 sensor circuit, bank 2 sensor 1",
		"P0155": "O2 sensor heater circuit, bank 2 sensor 1",
		"P0161": "O2 sensor heater circuit, bank 2 sensor 2",
		"P0171": "System too lean, bank 1",
		"P0172": "System too rich, bank 1",
		"P0174": "System too lean, bank 2",
		"P0175": "System too rich, bank 2",
		"P0191": "Fuel rail pressure sensor range/performance",
		"P0201": "Injector circuit, cylinder 1",
		"P0202": "Injector circuit, cylinder 2",
		"P0203": "Injector circuit, cylinder 3",
		"P0204": "Injector circuit, cylinder 4",
		"P0220": "Throttle/pedal position sensor B circuit",
		"P0234": "Turbocharger overboost condition",
		"P0299": "Turbocharger underboost condition",
		"P0300": "Random or multiple cylinder misfire detected",
		"P0325": "Knock sensor 1 circuit, bank 1",
		"P0327": "Knock sensor 1 circuit low, bank 1",
		"P0328": "Knock sensor 1 circuit high, bank 1",
		"P0335": "Crankshaft position sensor A circuit",
		"P0336": "Crankshaft position sensor A range/performance",
		"P0340": "Camshaft position sensor A circuit, bank 1",
		"P0341": "Camshaft position sensor A range/performance, bank 1",
		"P0351": "Ignition coil A primary/secondary circuit",
		"P0352": "Ignition coil B primary/secondary circuit",
		"P0353": "Ignition coil C primary/secondary circuit",
		"P0354": "Ignition coil D primary/secondary circuit",
		"P0400": "Exhaust gas recirculation flow",
		"P0401": "Exhaust gas recirculation flow insufficient",
		"P0402": "Exhaust gas recirculation flow excessive",
		"P0403": "Exhaust gas recirculation control circuit",
		"P0420": "Catalyst system efficiency below threshold, bank 1",
		"P0430": "Catalyst system efficiency below threshold, bank 2",
		"P0440": "Evaporative emission system",
		"P0441": "Evaporative emission system incorrect purge flow",
		"P0442": "Evaporative emission system leak detected (small)",
		"P0443": "Evaporative emission purge control valve circuit",
		"P0446": "Evaporative emission vent control circuit",
		"P0449": "Evaporative emission vent valve/solenoid circuit",
		"P0451": "Evaporative emission pressure sensor range/performance",
		"P0455": "Evaporative emission system leak detected (large)",
		"P0456": "Evaporative emission system leak detected (very small)",
		"P0457": "Evaporative emission leak detected (fuel cap loose/off)",
		"P0463": "Fuel level sensor circuit high",
		"P0480": "Cooling fan 1 control circuit",
		"P0500": "Vehicle speed sensor A",
		"P0505": "Idle control system",
		"P0506": "Idle control system RPM lower than expected",
		"P0507": "Idle control system RPM higher than expected",
		"P0560": "System voltage",
		"P0562": "System voltage low",
		"P0563": "System voltage high",
		"P0571": "Brake switch A circuit",
		"P0600": "Serial communication link",
		"P0601": "Internal control module memory checksum error",
		"P0603": "Internal control module keep alive memory error",
		"P0604": "Internal control module RAM error",
		"P0605": "Internal control module ROM error",
		"P0606": "Control module processor",
		"P0620": "Generator control circuit",
		"P0700": "Transmission control system (MIL request)",
		"P0705": "Transmission range sensor circuit",
		"P0715": "Input/turbine speed sensor A circuit",
		"P0720": "Output speed sensor circuit",
		"P0730": "Incorrect gear ratio",
		"P0740": "Torque converter clutch circuit",
		"P0741": "Torque converter clutch stuck off",
		"P0750": "Shift solenoid A",
		"P2096": "Post catalyst fuel trim system too lean, bank 1",
		"P2097": "Post catalyst fuel trim system too rich, bank 1",
		"P2135": "Throttle/pedal position sensor A/B voltage correlation",
		"P2138": "Throttle/pedal position sensor D/E voltage correlation",
		"P2195": "O2 sensor signal stuck lean, bank 1 sensor 1",
		"P2196": "O2 sensor signal stuck rich, bank 1 sensor 1",
		"U0001": "High speed CAN communication bus",
		"U0073": "Control module communication bus A off",
		"U0100": "Lost communication with ECM/PCM A",
		"U0101": "Lost communication with TCM",
		"U0121": "Lost communication with ABS control module",
		"U0140": "Lost communication with body control module",
		"U0155": "Lost communication with instrument panel cluster",
	}
	for cylinder := 1; cylinder <= 12; cylinder++ {
		m[fmt.Sprintf("P03%02d", cylinder)] = fmt.Sprintf("Cylinder %d misfire detected", cylinder)
	}
	return m
}()
