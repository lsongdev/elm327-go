// Command elm327-sim emulates a Wi-Fi ELM327 adapter over TCP, for developing
// OBD clients without a car.
//
//	elm327-sim -port 35000
//	elm327-sim -port 35000 -profile simulator/profiles/ford.json
package main

import (
	"flag"
	"fmt"
	"log"
	"net"

	"github.com/lsongdev/elm327-go/simulator"
)

func main() {
	port := flag.Int("port", 35000, "tcp port")
	host := flag.String("host", "0.0.0.0", "listen address")
	profilePath := flag.String("profile", "", "optional vehicle profile JSON")
	flag.Parse()

	var profile *simulator.Profile
	if *profilePath != "" {
		loaded, err := simulator.LoadProfile(*profilePath)
		if err != nil {
			log.Fatalf("load profile: %v", err)
		}
		profile = loaded
		log.Printf("loaded vehicle profile %q", profile.Vehicle)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *host, *port))
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	if err := simulator.NewVehicle(profile).Serve(listener, log.Default()); err != nil {
		log.Fatal(err)
	}
}
