// Command elm327-server polls a vehicle through a Wi-Fi ELM327 adapter and serves
// the live state over HTTP with a web UI.
//
//	elm327-server -adapter 192.168.0.10:35000
//	elm327-server -simulate
//	elm327-server -simulate -profile simulator/profiles/ford.json
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lsongdev/elm327-go/api"
	"github.com/lsongdev/elm327-go/simulator"
	"github.com/lsongdev/elm327-go/web"
)

func main() {
	adapter := flag.String("adapter", "", "ELM327 adapter address, host:port")
	simulate := flag.Bool("simulate", false, "use a built-in simulated vehicle instead of an adapter")
	profilePath := flag.String("profile", "", "vehicle profile JSON for -simulate")
	listen := flag.String("listen", ":8080", "HTTP listen address")
	interval := flag.Duration("interval", 500*time.Millisecond, "how often to poll the vehicle")
	token := flag.String("token", os.Getenv("ELM327_TOKEN"), "require this token (Bearer, or basic auth password)")
	allowClear := flag.Bool("allow-clear", false, "allow POST /api/dtc/clear to erase trouble codes on the vehicle")
	flag.Parse()
	if (*adapter == "") == !*simulate {
		log.Print("need exactly one of -adapter and -simulate")
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := *adapter
	if *simulate {
		listener, err := startSimulator(*profilePath)
		if err != nil {
			log.Fatal(err)
		}
		defer listener.Close()
		addr = listener.Addr().String()
	}

	monitor := api.NewMonitor(addr, api.MonitorOptions{Interval: *interval})
	go monitor.Run(ctx)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           web.Standalone(api.NewHandler(monitor, api.Options{AllowClear: *allowClear}), *token),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Printf("reading %s; api and web ui on http://%s", addr, *listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func startSimulator(profilePath string) (net.Listener, error) {
	var profile *simulator.Profile
	if profilePath != "" {
		loaded, err := simulator.LoadProfile(profilePath)
		if err != nil {
			return nil, err
		}
		profile = loaded
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go simulator.NewVehicle(profile).Serve(listener, nil)
	return listener, nil
}
