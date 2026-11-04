package simulator

import (
	"math"
	"math/rand"
	"time"
)

const defaultVIN = "1G1JC5444R7252367"

// drivePIDs are the Mode 01 PIDs of the built-in car.
var drivePIDs = []byte{
	0x01, 0x03, 0x04, 0x05, 0x06, 0x07, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x13, 0x14, 0x15,
	0x1C, 0x1F, 0x21, 0x2E, 0x2F, 0x30, 0x31, 0x33, 0x3C, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x49,
	0x4C, 0x51, 0x5C, 0x5E, 0x61, 0x62, 0xA6,
}

// freezePIDs are the PIDs stored in the built-in car's freeze frame.
var freezePIDs = []byte{0x02, 0x04, 0x05, 0x06, 0x07, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11}

// driveState is the built-in car at one moment.
type driveState struct {
	seconds  float64
	speed    float64 // km/h
	rpm      float64
	throttle float64 // %
	load     float64 // %
	maf      float64 // g/s
	manifold float64 // kPa
	timing   float64 // °
	coolant  float64 // °C
	oil      float64 // °C
	intake   float64 // °C
	catalyst float64 // °C
	fuel     float64 // %
	volts    float64
	distance float64 // km
}

const ambient = 22.0

// cycleSpeed is a repeating mix of city stops and faster stretches.
func cycleSpeed(t float64) float64 {
	v := 45 + 42*math.Sin(2*math.Pi*t/70) + 14*math.Sin(2*math.Pi*t/17)
	if v < 6 {
		return 0
	}
	return v
}

func (v *Vehicle) state(now time.Time) driveState {
	t := max(now.Sub(v.start).Seconds(), 0)
	speed := cycleSpeed(t)
	accel := cycleSpeed(t+1) - speed

	throttle := clamp(6+math.Max(accel, 0)*7+speed*0.12, 0, 100)
	rpm := 780.0
	if speed > 0 {
		// Five "gears" make the speed-to-rpm ratio drop as speed rises.
		gear := math.Min(math.Floor(speed/25)+1, 5)
		rpm = clamp(900+speed*(95/gear)+math.Max(accel, 0)*60, 900, 6200)
	}
	load := clamp(17+throttle*0.75, 0, 100)
	warm := 1 - math.Exp(-t/240)
	return driveState{
		seconds:  t,
		speed:    speed,
		rpm:      rpm,
		throttle: throttle,
		load:     load,
		maf:      rpm * load / 9000,
		manifold: clamp(28+load*0.7, 20, 101),
		timing:   clamp(26-load*0.25+math.Sin(t/3)*1.5, -10, 45),
		coolant:  ambient + (91-ambient)*warm,
		oil:      ambient + (98-ambient)*(1-math.Exp(-t/420)),
		intake:   ambient + 8 + load*0.1,
		catalyst: 180 + 520*warm + load*1.2,
		fuel:     math.Max(62-t*0.004, 3),
		volts:    13.9 + 0.15*math.Sin(t/5),
		distance: t * 45 / 3600,
	}
}

func clamp(v, low, high float64) float64 { return min(max(v, low), high) }

// driveData encodes one PID of the built-in car. Callers hold v.mu.
func (v *Vehicle) driveData(pid byte, d driveState) []byte {
	noise := func(amount float64) float64 { return (rand.Float64() - 0.5) * 2 * amount }
	value := func(x float64) []byte { return encodeValue(pid, x) }
	switch pid {
	case 0x01:
		return v.monitorBytes()
	case 0x02:
		return v.freezeData(pid)
	case 0x03:
		if d.coolant < 45 {
			return []byte{0x01, 0x00}
		}
		return []byte{0x02, 0x00}
	case 0x04:
		return value(d.load)
	case 0x05:
		return value(d.coolant)
	case 0x06:
		return value(noise(3))
	case 0x07:
		return value(4.7)
	case 0x0B:
		return value(d.manifold)
	case 0x0C:
		return value(d.rpm + noise(12))
	case 0x0D:
		return value(d.speed)
	case 0x0E:
		return value(d.timing)
	case 0x0F:
		return value(d.intake)
	case 0x10:
		return value(d.maf)
	case 0x11:
		return value(d.throttle)
	case 0x13:
		return []byte{0x03} // bank 1, sensors 1 and 2
	case 0x14:
		return []byte{byte(90 + 70*math.Sin(d.seconds*2)), 0x80 + byte(noise(4)+4)}
	case 0x15:
		return []byte{150, 0xFF} // downstream sensor: not used for trim
	case 0x1C:
		return value(6) // EOBD
	case 0x1F:
		return value(d.seconds)
	case 0x21:
		if len(v.stored) == 0 {
			return value(0)
		}
		return value(38)
	case 0x2E:
		return value(clamp(d.load*0.3, 0, 100))
	case 0x2F:
		return value(d.fuel)
	case 0x30:
		return value(12)
	case 0x31:
		return value(1840 + d.distance)
	case 0x33:
		return value(101)
	case 0x3C:
		return value(d.catalyst)
	case 0x42:
		return value(d.volts)
	case 0x43:
		return value(d.load * 0.92)
	case 0x44:
		return value(1 + noise(0.01))
	case 0x45:
		return value(clamp(d.throttle-4, 0, 100))
	case 0x46:
		return value(ambient)
	case 0x47:
		return value(clamp(d.throttle+14, 0, 100))
	case 0x49:
		return value(clamp(d.throttle*1.1+11, 0, 100))
	case 0x4C:
		return value(d.throttle)
	case 0x51:
		return value(1) // gasoline
	case 0x5C:
		return value(d.oil)
	case 0x5E:
		return value(d.maf * 3600 / (14.7 * 745))
	case 0x61:
		return value(clamp(d.load*0.9, -125, 130))
	case 0x62:
		return value(clamp(d.load*0.85, -125, 130))
	case 0xA6:
		return value(48210 + d.distance)
	}
	return nil
}

// monitorBytes builds PID 01. A clear leaves every monitor incomplete, as on a
// real car, until a drive cycle finishes them.
func (v *Vehicle) monitorBytes() []byte {
	a := byte(len(v.stored))
	if len(v.stored) > 0 {
		a |= 0x80
	}
	const available = 0xE5 // catalyst, evap, O2 sensor, O2 heater, EGR
	b, d := byte(0x07), byte(0x80)
	if v.cleared {
		b, d = 0x07|0x20, available
	}
	return []byte{a, b, available, d}
}

// encodeValue converts an engineering value to a PID's data bytes. It is the
// inverse of the formulas in package obd, for PIDs with a single value.
func encodeValue(pid byte, value float64) []byte {
	one := func(x float64) []byte { return []byte{byte(clamp(math.Round(x), 0, 255))} }
	two := func(x float64) []byte {
		raw := uint16(clamp(math.Round(x), 0, 65535))
		return []byte{byte(raw >> 8), byte(raw)}
	}
	switch pid {
	case 0x04, 0x11, 0x2C, 0x2E, 0x2F, 0x45, 0x47, 0x48, 0x49, 0x4A, 0x4B, 0x4C, 0x52, 0x5A, 0x5B:
		return one(value * 255 / 100)
	case 0x05, 0x0F, 0x46, 0x5C:
		return one(value + 40)
	case 0x06, 0x07, 0x08, 0x09, 0x2D:
		return one((value + 100) * 128 / 100)
	case 0x0A:
		return one(value / 3)
	case 0x0B, 0x0D, 0x1C, 0x30, 0x33, 0x51:
		return one(value)
	case 0x0C:
		return two(value * 4)
	case 0x0E:
		return one((value + 64) * 2)
	case 0x10:
		return two(value * 100)
	case 0x1F, 0x21, 0x31, 0x4D, 0x4E, 0x63:
		return two(value)
	case 0x3C, 0x3D, 0x3E, 0x3F:
		return two((value + 40) * 10)
	case 0x42:
		return two(value * 1000)
	case 0x43:
		return two(value * 255 / 100)
	case 0x44:
		return two(value * 32768)
	case 0x5E:
		return two(value * 20)
	case 0x61, 0x62, 0x8E:
		return one(value + 125)
	case 0xA6:
		raw := uint32(clamp(math.Round(value*10), 0, math.MaxUint32))
		return []byte{byte(raw >> 24), byte(raw >> 16), byte(raw >> 8), byte(raw)}
	}
	return nil
}
