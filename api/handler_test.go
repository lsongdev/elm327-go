package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lsongdev/elm327-go/elm327"
	"github.com/lsongdev/elm327-go/simulator"
)

// start runs a monitor against a simulated vehicle and waits until it has the
// metrics a client would first ask for.
func start(t *testing.T, profile *simulator.Profile) (*Monitor, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go simulator.NewVehicle(profile).Serve(listener, nil)
	m := NewMonitor(listener.Addr().String(), MonitorOptions{
		Interval:      5 * time.Millisecond,
		RetryInterval: 10 * time.Millisecond,
		ProbeInterval: 10 * time.Millisecond,
		Logger:        log.New(io.Discard, "", 0),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	stop := func() {
		cancel()
		listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("monitor did not stop")
		}
	}
	t.Cleanup(stop)
	waitFor(t, "first metrics", func() bool {
		snap := m.Snapshot()
		return snap.Status.State == StateOnline && len(snap.Metrics) >= len(fastPIDs)
	})
	return m, stop
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func get(t *testing.T, h http.Handler, path string, v any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("GET %s: %v: %s", path, err, w.Body)
		}
	}
	return w
}

func post(t *testing.T, h http.Handler, path, body string, v any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	if v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("POST %s: %v: %s", path, err, w.Body)
		}
	}
	return w
}

func TestState(t *testing.T) {
	m, _ := start(t, nil)
	h := NewHandler(m, Options{})
	var snap Snapshot
	if w := get(t, h, "/state", &snap); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if snap.Status.ClearEnabled {
		t.Error("clearing must be off by default")
	}
	if snap.Vehicle.VIN != "1G1JC5444R7252367" || snap.Adapter.Version == "" {
		t.Errorf("identity = %+v %+v", snap.Vehicle, snap.Adapter)
	}
	if d := snap.Diagnostics; d.Monitor == nil || !d.Monitor.MIL || len(d.Stored) != 2 || len(d.Pending) != 1 || len(d.Permanent) != 1 {
		t.Errorf("diagnostics = %+v", d)
	}
	keys := map[string]Metric{}
	for _, metric := range snap.Metrics {
		keys[metric.Key] = metric
	}
	rpm, ok := keys["engine_rpm"]
	if !ok || rpm.Value == nil || *rpm.Value < 700 || rpm.Unit != "rpm" || rpm.Max != 8000 || rpm.Updated.IsZero() {
		t.Errorf("rpm = %+v", rpm)
	}
	for _, s := range snap.Supported {
		if len(s) != 2 {
			t.Errorf("supported entry %q", s)
		}
	}
}

func TestSlowPIDsRotateIn(t *testing.T) {
	m, _ := start(t, nil)
	waitFor(t, "slow PIDs", func() bool {
		for _, metric := range m.Snapshot().Metrics {
			if metric.Key == "odometer" {
				return true
			}
		}
		return false
	})
}

func TestPIDs(t *testing.T) {
	m, _ := start(t, nil)
	h := NewHandler(m, Options{})
	var list []PIDInfo
	get(t, h, "/pids", &list)
	found := map[string]PIDInfo{}
	for _, p := range list {
		found[p.PID] = p
	}
	if p := found["0C"]; !p.Supported || p.Key != "engine_rpm" {
		t.Errorf("0C = %+v", p)
	}
	if p := found["0A"]; p.Supported || p.Key != "fuel_pressure" {
		t.Errorf("0A = %+v", p)
	}

	var readings []struct {
		ECU   string   `json:"ecu"`
		Key   string   `json:"key"`
		Value *float64 `json:"value"`
	}
	if w := get(t, h, "/pids/33", &readings); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if len(readings) != 1 || readings[0].ECU != "7E8" || *readings[0].Value != 101 {
		t.Errorf("readings = %+v", readings)
	}
	if w := get(t, h, "/pids/ZZ", nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad pid status %d", w.Code)
	}
	// Advertised by nothing: the vehicle answers NO DATA.
	if w := get(t, h, "/pids/0A", nil); w.Code != http.StatusNotFound {
		t.Errorf("unsupported pid status %d: %s", w.Code, w.Body)
	}
}

func TestDiagnosticsEndpoints(t *testing.T) {
	m, _ := start(t, nil)
	h := NewHandler(m, Options{})
	var d Diagnostics
	if w := get(t, h, "/dtc?refresh=1", &d); w.Code != http.StatusOK || len(d.Stored) != 2 {
		t.Fatalf("dtc %d: %s", w.Code, w.Body)
	}
	var frame struct {
		DTC      struct{ Code string }  `json:"dtc"`
		Readings []struct{ Key string } `json:"readings"`
	}
	if w := get(t, h, "/freeze-frame", &frame); w.Code != http.StatusOK || frame.DTC.Code != "P0301" || len(frame.Readings) == 0 {
		t.Fatalf("freeze frame %d: %s", w.Code, w.Body)
	}
	var vehicle struct {
		VIN string `json:"vin"`
	}
	if get(t, h, "/vehicle?refresh=1", &vehicle); vehicle.VIN == "" {
		t.Error("no VIN")
	}
}

func TestClearIsGated(t *testing.T) {
	m, _ := start(t, nil)
	disabled := NewHandler(m, Options{})
	if w := post(t, disabled, "/dtc/clear", `{"confirm":true}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("disabled clear status %d", w.Code)
	}
	if snap := m.Snapshot(); len(snap.Diagnostics.Stored) != 2 {
		t.Fatalf("disabled clear changed the vehicle: %+v", snap.Diagnostics)
	}

	h := NewHandler(m, Options{AllowClear: true})
	if w := post(t, h, "/dtc/clear", `{}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed clear status %d", w.Code)
	}
	if snap := m.Snapshot(); len(snap.Diagnostics.Stored) != 2 {
		t.Fatal("an unconfirmed clear changed the vehicle")
	}
	var d Diagnostics
	if w := post(t, h, "/dtc/clear", `{"confirm":true}`, &d); w.Code != http.StatusOK {
		t.Fatalf("clear status %d: %+v", w.Code, d)
	}
	if len(d.Stored) != 0 || len(d.Pending) != 0 || len(d.Permanent) != 1 || d.Monitor.MIL {
		t.Errorf("after clear = %+v", d)
	}
	var snap Snapshot
	if get(t, h, "/state", &snap); !snap.Status.ClearEnabled {
		t.Error("status should say clearing is enabled")
	}
}

func TestCommandAllowlist(t *testing.T) {
	m, _ := start(t, nil)
	h := NewHandler(m, Options{AllowClear: true}) // not even -allow-clear opens this
	for _, command := range []string{"04", "0 4", "ATZ", "ATSP3", "ATH0", "ATMA", "2E F190 00", "2F 01 02", "31 01", "3E00", "10 03"} {
		body, _ := json.Marshal(commandRequest{Command: command})
		if w := post(t, h, "/command", string(body), nil); w.Code != http.StatusForbidden {
			t.Errorf("%q status %d: %s", command, w.Code, w.Body)
		}
	}
	for _, command := range []string{"", "XYZ", "010", "0100000000000000000000"} {
		body, _ := json.Marshal(commandRequest{Command: command})
		if w := post(t, h, "/command", string(body), nil); w.Code != http.StatusBadRequest {
			t.Errorf("%q status %d: %s", command, w.Code, w.Body)
		}
	}
	if snap := m.Snapshot(); len(snap.Diagnostics.Stored) != 2 {
		t.Fatal("a refused command changed the vehicle")
	}

	var result commandResult
	if w := post(t, h, "/command", `{"command":"01 33"}`, &result); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if result.Command != "0133" || len(result.Readings) != 1 || *result.Readings[0].Value != 101 {
		t.Errorf("result = %+v", result)
	}
	if post(t, h, "/command", `{"command":"03"}`, &result); len(result.DTCs) != 2 {
		t.Errorf("dtcs = %+v", result)
	}
	result = commandResult{}
	if post(t, h, "/command", `{"command":"atrv"}`, &result); !strings.HasSuffix(result.Response, "V") {
		t.Errorf("atrv = %+v", result)
	}
}

func TestDecode(t *testing.T) {
	m, _ := start(t, nil)
	h := NewHandler(m, Options{})
	var d Decoded
	post(t, h, "/decode", `{"response":"7E8 04 41 0C 1A F8"}`, &d)
	if len(d.Readings) != 1 || *d.Readings[0].Value != 1726 || d.Readings[0].ECU != "7E8" || d.Messages[0].Data != "410C1AF8" {
		t.Errorf("decoded = %+v", d)
	}
	d = Decoded{}
	post(t, h, "/decode", `{"response":"NO DATA"}`, &d)
	if d.Error == "" {
		t.Errorf("decoded = %+v", d)
	}
	if w := post(t, h, "/decode", `{}`, nil); w.Code != http.StatusBadRequest {
		t.Errorf("empty status %d", w.Code)
	}
}

func TestLog(t *testing.T) {
	m, _ := start(t, nil)
	h := NewHandler(m, Options{})
	var entries []Entry
	get(t, h, "/log?limit=5", &entries)
	if len(entries) != 5 {
		t.Fatalf("entries = %d", len(entries))
	}
	last := entries[len(entries)-1].Seq
	waitFor(t, "new entries", func() bool {
		var newer []Entry
		get(t, h, "/log?after="+strings.TrimSpace(jsonNumber(last)), &newer)
		return len(newer) > 0 && newer[0].Seq > last
	})
	if w := get(t, h, "/log?after=x", nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad after status %d", w.Code)
	}
}

func jsonNumber(v uint64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestEvents(t *testing.T) {
	m, _ := start(t, nil)
	srv := httptest.NewServer(NewHandler(m, Options{Interval: 5 * time.Millisecond}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events?log=1", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	seen := map[string]int{}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		if event, ok := strings.CutPrefix(scanner.Text(), "event: "); ok {
			seen[event]++
		}
		if seen["state"] >= 2 && seen["log"] >= 1 {
			return
		}
	}
	t.Fatalf("events = %v, err = %v", seen, scanner.Err())
}

func TestToken(t *testing.T) {
	m, _ := start(t, nil)
	h := NewHandler(m, Options{Token: "s3cret"})
	if w := get(t, h, "/status", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no token status %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	r.Header.Set("Authorization", "Bearer s3cret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("token status %d", w.Code)
	}
}

func TestReconnects(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	vehicle := simulator.NewVehicle(nil)
	var conns = make(chan net.Conn, 16)
	counted := countingListener{Listener: listener, conns: conns}
	go vehicle.Serve(counted, nil)
	m := NewMonitor(addr, MonitorOptions{Interval: 5 * time.Millisecond, RetryInterval: 10 * time.Millisecond, Logger: log.New(io.Discard, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	defer listener.Close()
	waitFor(t, "online", func() bool { return m.Snapshot().Status.State == StateOnline })

	// Drop the connection, as a Wi-Fi adapter does when the car is switched off.
	(<-conns).Close()
	waitFor(t, "reconnect", func() bool {
		select {
		case <-conns:
			return true
		default:
			return false
		}
	})
	before := m.Snapshot().Status.Polls
	waitFor(t, "polling again", func() bool {
		snap := m.Snapshot()
		return snap.Status.State == StateOnline && snap.Status.Polls > before
	})
}

type countingListener struct {
	net.Listener
	conns chan net.Conn
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.conns <- c
	}
	return c, err
}

func TestVehicleOffThenOn(t *testing.T) {
	// A profile with nothing that answers stands in for a car with the ignition off.
	off := &simulator.Profile{Vehicle: "off", PIDs: map[string]simulator.PIDProfile{"0100": {Mode: "unsupported"}}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go simulator.NewVehicle(off).Serve(listener, nil)
	m := NewMonitor(listener.Addr().String(), MonitorOptions{ProbeInterval: 5 * time.Millisecond, Logger: log.New(io.Discard, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	waitFor(t, "no vehicle", func() bool {
		s := m.Snapshot().Status
		return s.State == StateNoVehicle && s.Error != ""
	})
	h := NewHandler(m, Options{})
	if w := get(t, h, "/pids/0C", nil); w.Code != http.StatusNotFound {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
}

// A vehicle that advertises PIDs and then stops answering, as when the ignition
// goes off, must leave the online state even after every PID is out of rotation.
func TestVehicleStopsAnswering(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var probes atomic.Int32
	server := &elm327.Server{
		Logger: log.New(io.Discard, "", 0),
		Handler: func(command string) (string, bool) {
			switch {
			case strings.HasPrefix(command, "AT"):
				return "OK", true
			case command == "0100":
				probes.Add(1)
				return "41 00 00 10 00 00", true // only PID 0C
			}
			return "NO DATA", true
		},
	}
	go server.Serve(listener)
	m := NewMonitor(listener.Addr().String(), MonitorOptions{Interval: time.Millisecond, Logger: log.New(io.Discard, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	waitFor(t, "polling to give up and probe again", func() bool { return probes.Load() >= 2 })
}

func TestNotConnected(t *testing.T) {
	// Nothing listens here: the monitor stays connecting and requests get 503.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	m := NewMonitor(addr, MonitorOptions{RetryInterval: 5 * time.Millisecond, Logger: log.New(io.Discard, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	waitFor(t, "connection error", func() bool { return m.Snapshot().Status.Error != "" })
	h := NewHandler(m, Options{})
	if w := get(t, h, "/pids/0C", nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d: %s", w.Code, w.Body)
	}
	if w := post(t, h, "/command", `{"command":"010C"}`, nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("command status %d: %s", w.Code, w.Body)
	}
}
