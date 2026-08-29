package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	elm327 "github.com/lsongdev/elm327-go/elm327"
)

func enterAltScreen() {
	// Alternate screen buffer + hide cursor.
	// fmt.Print("\033[?1049h\033[?25l")
}

func exitAltScreen() {
	// Show cursor + restore normal screen buffer.
	fmt.Print("\033[?25h\033[?1049l")
}

func moveHome() {
	// Move cursor to home (top-left) without clearing the whole screen.
	fmt.Print("\033[H")
}

func clearLine() {
	// Clear the current line.
	fmt.Print("\033[2K")
}

func printlnf(format string, args ...any) {
	clearLine()
	fmt.Printf(format, args...)
	fmt.Print("\n")
}

func main() {

	var host, port string
	flag.StringVar(&host, "host", "192.168.3.178", "host")
	flag.StringVar(&port, "port", "35000", "port")
	flag.Parse()

	log.Println("Connecting to", fmt.Sprintf("%s:%s", host, port))
	client := &elm327.Client{}
	err := client.Connect(fmt.Sprintf("%s:%s", host, port))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(); err != nil {
		log.Fatalf("Initialize adapter: %v", err)
	}

	enterAltScreen()
	defer exitAltScreen()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		moveHome()

		rpm, rpmErr := client.EngineRPM()
		speed, speedErr := client.VehicleSpeed()
		coolant, coolantErr := client.EngineCoolantTemp()
		intake, intakeErr := client.IntakeAirTemp()
		ambient, ambientErr := client.AmbientAirTemp()

		load, loadErr := client.EngineLoad()
		throttle, throttleErr := client.ThrottlePosition()
		mapKPa, mapErr := client.IntakeMAP()
		maf, mafErr := client.MAF()
		timing, timingErr := client.TimingAdvance()

		fuel, fuelErr := client.FuelLevel()
		voltage, voltageErr := client.ControlModuleVoltage()
		runtime, runtimeErr := client.RuntimeSinceEngineStart()
		baro, baroErr := client.BarometricPressure()

		now := time.Now().Format("2006-01-02 15:04:05")
		printlnf("ELM327 live metrics  (%s) \t %s:%s", now, host, port)
		fmt.Print("\n")

		printlnf("%-26s %12s   %s", "Metric", "Value", "Error")
		printlnf("%-26s %12s   %s", "--------------------------", "------------", "-----")

		printlnf("%-26s %12.0f   %v", "Engine RPM", rpm, rpmErr)
		printlnf("%-26s %12d   %v", "Vehicle Speed (km/h)", speed, speedErr)
		printlnf("%-26s %12d   %v", "Coolant Temp (°C)", coolant, coolantErr)
		printlnf("%-26s %12d   %v", "Intake Air Temp (°C)", intake, intakeErr)
		printlnf("%-26s %12d   %v", "Ambient Air Temp (°C)", ambient, ambientErr)
		fmt.Print("\n")
		printlnf("%-26s %11.1f%%   %v", "Engine Load", load, loadErr)
		printlnf("%-26s %11.1f%%   %v", "Throttle Position", throttle, throttleErr)
		printlnf("%-26s %12d   %v", "Intake MAP (kPa)", mapKPa, mapErr)
		printlnf("%-26s %12.2f   %v", "MAF (g/s)", maf, mafErr)
		printlnf("%-26s %12.1f   %v", "Timing Advance (°)", timing, timingErr)
		fmt.Print("\n")
		printlnf("%-26s %11.1f%%   %v", "Fuel Level", fuel, fuelErr)
		printlnf("%-26s %12.3f   %v", "Module Voltage (V)", voltage, voltageErr)
		printlnf("%-26s %12d   %v", "Runtime (s)", runtime, runtimeErr)
		printlnf("%-26s %12d   %v", "Barometric (kPa)", baro, baroErr)
	}
}
