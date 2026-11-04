package obd

import "fmt"

// Monitor is the state of one OBD readiness test.
type Monitor struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Complete  bool   `json:"complete"` // meaningful only when Available
}

// MonitorStatus is PID 01: the MIL, the stored DTC count and readiness monitors.
type MonitorStatus struct {
	MIL      bool      `json:"mil"`
	DTCCount int       `json:"dtc_count"`
	Ignition string    `json:"ignition"` // "spark" or "compression"
	Monitors []Monitor `json:"monitors"`
}

// Ready reports whether every available monitor has completed.
func (s MonitorStatus) Ready() bool {
	for _, m := range s.Monitors {
		if m.Available && !m.Complete {
			return false
		}
	}
	return true
}

// In byte B the low three bits say a continuous test exists and the next three
// say it is incomplete, in this order.
var continuousMonitors = []Monitor{
	{Key: "misfire", Name: "Misfire"},
	{Key: "fuel_system", Name: "Fuel system"},
	{Key: "components", Name: "Comprehensive components"},
}

// Bytes C (available) and D (incomplete) depend on the ignition type, bit 0 first.
var sparkMonitors = []Monitor{
	{Key: "catalyst", Name: "Catalyst"},
	{Key: "heated_catalyst", Name: "Heated catalyst"},
	{Key: "evap", Name: "Evaporative system"},
	{Key: "secondary_air", Name: "Secondary air system"},
	{Key: "ac_refrigerant", Name: "A/C refrigerant"},
	{Key: "o2_sensor", Name: "O2 sensor"},
	{Key: "o2_heater", Name: "O2 sensor heater"},
	{Key: "egr", Name: "EGR system"},
}

var compressionMonitors = []Monitor{
	{Key: "nmhc_catalyst", Name: "NMHC catalyst"},
	{Key: "nox_scr", Name: "NOx/SCR aftertreatment"},
	{},
	{Key: "boost_pressure", Name: "Boost pressure"},
	{},
	{Key: "exhaust_sensor", Name: "Exhaust gas sensor"},
	{Key: "pm_filter", Name: "PM filter"},
	{Key: "egr_vvt", Name: "EGR/VVT system"},
}

// ParseMonitorStatus decodes the 4 data bytes of PID 01. PID 41 has the same
// layout for bytes B to D and reports this drive cycle.
func ParseMonitorStatus(b []byte) (MonitorStatus, error) {
	if len(b) < 4 {
		return MonitorStatus{}, fmt.Errorf("%w: monitor status needs 4 bytes, got %d", ErrShortData, len(b))
	}
	s := MonitorStatus{
		MIL:      b[0]&0x80 != 0,
		DTCCount: int(b[0] & 0x7F),
		Ignition: "spark",
		Monitors: []Monitor{},
	}
	table := sparkMonitors
	if b[1]&0x08 != 0 {
		s.Ignition = "compression"
		table = compressionMonitors
	}
	for i, m := range continuousMonitors {
		m.Available = b[1]&(1<<i) != 0
		m.Complete = b[1]&(0x10<<i) == 0
		s.Monitors = append(s.Monitors, m)
	}
	for i, m := range table {
		if m.Key == "" {
			continue // reserved
		}
		m.Available = b[2]&(1<<i) != 0
		m.Complete = b[3]&(1<<i) == 0
		s.Monitors = append(s.Monitors, m)
	}
	return s, nil
}

func decodeMonitorStatus(b []byte, r *Reading) {
	s, err := ParseMonitorStatus(b)
	if err != nil {
		return
	}
	state := "MIL off"
	if s.MIL {
		state = "MIL on"
	}
	r.Value = ptr(float64(s.DTCCount))
	r.Text = fmt.Sprintf("%s, %d DTC", state, s.DTCCount)
	mil := 0.0
	if s.MIL {
		mil = 1
	}
	r.Fields = []Field{
		{Key: "mil", Name: "MIL", Value: ptr(mil), Text: state},
		num("dtc_count", "Stored DTCs", "", float64(s.DTCCount)),
	}
}

func decodeMonitorCycle(b []byte, r *Reading) {
	s, err := ParseMonitorStatus(b)
	if err != nil {
		return
	}
	incomplete := 0
	for _, m := range s.Monitors {
		if m.Available && !m.Complete {
			incomplete++
		}
	}
	r.Value = ptr(float64(incomplete))
	r.Text = fmt.Sprintf("%d incomplete", incomplete)
}
