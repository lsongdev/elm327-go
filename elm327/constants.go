package elm327

// OBD2 PIDs (Mode 01)
const (
	// General PIDs
	PIDSupportedPIDs00to1F       = "0100" // Supported PIDs 01-1F (bitmask)
	PIDDTCTroubleCodes           = "0101" // Status codes since DTCs cleared
	PIDFreezeDTC                 = "0102" // Freeze DTC
	PIDFuelSystemStatus          = "0103" // Fuel system status
	PIDEngineLoad                = "0104" // Engine load % (A*100/255)
	PIDEngineCoolantTemp         = "0105" // Engine coolant temperature °C (A-40)
	PIDShortTermFuelTrimBank1    = "0106" // Short term fuel trim bank 1 % ((A-128)*100/128)
	PIDLongTermFuelTrimBank1     = "0107" // Long term fuel trim bank 1 % ((A-128)*100/128)
	PIDShortTermFuelTrimBank2    = "0108" // Short term fuel trim bank 2 % ((A-128)*100/128)
	PIDLongTermFuelTrimBank2     = "0109" // Long term fuel trim bank 2 % ((A-128)*100/128)
	PIDFuelPressure              = "010A" // Fuel pressure kPa (A*3)
	PIDIntakeMAP                 = "010B" // Intake manifold pressure kPa (A)
	PIDEngineRPM                 = "010C" // Engine speed rpm ((A*256+B)/4)
	PIDVehicleSpeed              = "010D" // Vehicle speed km/h (A)
	PIDTimingAdvance             = "010E" // Timing advance ° ((A-128)/2)
	PIDIntakeAirTemp             = "010F" // Intake air temperature °C (A-40)
	PIDMAF                       = "0110" // MAF air flow rate g/s ((A*256+B)/100)
	PIDThrottlePosition          = "0111" // Throttle position % (A*100/255)
	PIDIdleAirControl            = "0112" // Idle air control % (A*100/255)
	PIDO2SensorLocation          = "0113" // O2 sensor location
	PIDO2Sensor1Voltage          = "0114" // O2 sensor 1 voltage V (A/200)
	PIDO2Sensor2Voltage          = "0115" // O2 sensor 2 voltage V (A/200)
	PIDVehicleSpeed2             = "011C" // Vehicle speed km/h (A)
	PIDRuntimeSinceEngineStart   = "011F" // Runtime since engine start s (A*256+B)
	PIDEvaporativeSystemPressure = "0121" // Evaporative system pressure Pa ((A*256+B)-32767)
	PIDFuelLevel                 = "012F" // Fuel level % (A*100/255)
	PIDDistanceSinceColdStart    = "0131" // Distance since cold start km (A*256+B)
	PIDBarometricPressure        = "0133" // Absolute fuel pressure kPa ((A*256+B)*10)
	PIDControlModuleVoltage      = "0142" // Control module voltage V ((A*256+B)/1000)
	PIDAmbientAirTemp            = "0146" // Ambient air temperature °C (A-40)
	PIDFuelType                  = "0151" // Fuel type
	PIDEthanolFuelPercentage     = "015A" // Ethanol fuel % (A*100/255)
)
