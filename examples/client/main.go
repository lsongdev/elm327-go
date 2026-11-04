// Command client prints every value a vehicle advertises, once per second, using
// the elm327 client directly.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"slices"
	"time"

	"github.com/lsongdev/elm327-go/elm327"
	"github.com/lsongdev/elm327-go/obd"
)

func main() {
	host := flag.String("host", "192.168.3.178", "adapter host")
	port := flag.String("port", "35000", "adapter port")
	once := flag.Bool("once", false, "print once and exit")
	flag.Parse()

	addr := net.JoinHostPort(*host, *port)
	log.Println("Connecting to", addr)
	client := &elm327.Client{}
	if err := client.Connect(addr); err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(); err != nil {
		log.Fatalf("initialize adapter: %v", err)
	}
	pids, err := client.SupportedPIDs()
	if err != nil {
		log.Fatalf("read supported PIDs: %v", err)
	}
	pids = slices.DeleteFunc(pids, obd.IsBitmap)

	if info, err := client.VehicleInfo(); err == nil && info.VIN != "" {
		fmt.Println("VIN:", info.VIN)
	}
	for _, kind := range []obd.DTCKind{obd.DTCStored, obd.DTCPending} {
		codes, err := client.ReadDTCs(kind)
		if err != nil {
			log.Fatalf("read trouble codes: %v", err)
		}
		for _, code := range codes {
			fmt.Printf("DTC %s  %s\n", code.Code, code.Description)
		}
	}

	for {
		fmt.Printf("\n%s\n", time.Now().Format("15:04:05"))
		for _, pid := range pids {
			reading, err := client.ReadPID(pid)
			if err != nil {
				fmt.Printf("%02X  %-44s %v\n", pid, "", err)
				continue
			}
			fmt.Printf("%02X  %-44s %s\n", pid, reading.Name, format(reading))
		}
		if *once {
			return
		}
		time.Sleep(time.Second)
	}
}

func format(r obd.Reading) string {
	switch {
	case r.Text != "":
		return r.Text
	case r.Value != nil:
		return fmt.Sprintf("%.2f %s", *r.Value, r.Unit)
	}
	return r.Raw
}
