// Package api polls a vehicle through an elm327.Client and serves the live state
// and on-demand diagnostics over HTTP.
package api

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/lsongdev/elm327-go/elm327"
	"github.com/lsongdev/elm327-go/obd"
)

const (
	// logSize is how many recent exchanges the monitor keeps.
	logSize = 500
	// slowPerTick is how many non-fast PIDs each tick reads, in rotation.
	slowPerTick = 2
	// maxPIDFailures takes a PID out of polling for this connection; some ECUs
	// advertise PIDs they do not answer.
	maxPIDFailures = 3
	// lostAfterTicks is how many ticks of nothing but failures mean the vehicle
	// went away, usually because the ignition was turned off.
	lostAfterTicks = 6
)

// fastPIDs change quickly and are read on every tick.
var fastPIDs = []byte{0x0C, 0x0D, 0x04, 0x11, 0x10, 0x0B, 0x0E}

// State is the monitor's connection to the vehicle.
type State string

const (
	StateConnecting State = "connecting" // reaching the adapter
	StateSearching  State = "searching"  // adapter reached, looking for the vehicle
	StateNoVehicle  State = "no_vehicle" // adapter reached, the vehicle does not answer
	StateOnline     State = "online"     // polling
)

// Status describes the data source.
type Status struct {
	Adapter      string    `json:"adapter"` // address
	State        State     `json:"state"`
	Error        string    `json:"error,omitempty"` // why it is not online
	Started      time.Time `json:"started"`
	LastUpdate   time.Time `json:"last_update"`
	Polls        uint64    `json:"polls"`
	Errors       uint64    `json:"errors"`
	ClearEnabled bool      `json:"clear_enabled"` // POST /dtc/clear is allowed
}

// Metric is the latest value of a PID.
type Metric struct {
	obd.Reading
	Min     float64   `json:"min"`
	Max     float64   `json:"max"`
	Updated time.Time `json:"updated"`
}

// Diagnostics is the vehicle's fault state.
type Diagnostics struct {
	Monitor   *obd.MonitorStatus `json:"monitor,omitempty"`
	Stored    []obd.DTC          `json:"stored"`
	Pending   []obd.DTC          `json:"pending"`
	Permanent []obd.DTC          `json:"permanent"`
	Updated   time.Time          `json:"updated"`
}

// Snapshot is the whole live state.
type Snapshot struct {
	Version     uint64             `json:"version"`
	Status      Status             `json:"status"`
	Adapter     elm327.AdapterInfo `json:"adapter"`
	Vehicle     obd.VehicleInfo    `json:"vehicle"`
	Supported   []string           `json:"supported"` // Mode 01 value PIDs, two hex digits
	Metrics     []Metric           `json:"metrics"`
	Diagnostics Diagnostics        `json:"diagnostics"`
}

// Entry is one command exchanged with the adapter.
type Entry struct {
	Seq        uint64    `json:"seq"`
	Time       time.Time `json:"time"`
	Command    string    `json:"command"`
	Response   string    `json:"response,omitempty"`
	DurationMS float64   `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
}

// MonitorOptions configures a Monitor. Zero values take the defaults.
type MonitorOptions struct {
	Interval            time.Duration // between polls; 500ms
	DiagnosticsInterval time.Duration // between fault code reads; 30s
	RetryInterval       time.Duration // between attempts to reach the adapter; 3s
	ProbeInterval       time.Duration // between looks for a vehicle that is off; 10s
	Logger              *log.Logger
}

// Monitor keeps a connection to one adapter and polls the vehicle behind it.
// On-demand requests go through Client, which runs them between polls.
type Monitor struct {
	addr   string
	opts   MonitorOptions
	client *elm327.Client
	logger *log.Logger

	mu          sync.RWMutex
	version     uint64
	status      Status
	adapter     elm327.AdapterInfo
	vehicle     obd.VehicleInfo
	supported   []byte
	metrics     map[byte]Metric
	diagnostics Diagnostics
	entries     []Entry // ring buffer
	next        int
	seq         uint64
	subs        map[chan Entry]struct{}
}

// NewMonitor returns a monitor for the adapter at addr. Call Run to start it.
func NewMonitor(addr string, opts MonitorOptions) *Monitor {
	if opts.Interval <= 0 {
		opts.Interval = 500 * time.Millisecond
	}
	if opts.DiagnosticsInterval <= 0 {
		opts.DiagnosticsInterval = 30 * time.Second
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = 3 * time.Second
	}
	if opts.ProbeInterval <= 0 {
		opts.ProbeInterval = 10 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	m := &Monitor{
		addr:    addr,
		opts:    opts,
		logger:  logger,
		status:  Status{Adapter: addr, State: StateConnecting, Started: time.Now()},
		metrics: map[byte]Metric{},
		diagnostics: Diagnostics{
			Stored: []obd.DTC{}, Pending: []obd.DTC{}, Permanent: []obd.DTC{},
		},
		subs: map[chan Entry]struct{}{},
	}
	m.client = &elm327.Client{Trace: m.trace}
	return m
}

// Client returns the client the monitor polls with, for on-demand requests.
func (m *Monitor) Client() *elm327.Client { return m.client }

// Run connects and polls until ctx ends, reconnecting whenever the adapter is lost.
func (m *Monitor) Run(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { m.client.Close() })
	defer stop()
	for ctx.Err() == nil {
		m.setState(StateConnecting, nil)
		err := m.client.ConnectContext(ctx, m.addr)
		if err == nil {
			err = m.client.Initialize()
		}
		if err == nil {
			err = m.session(ctx)
		}
		m.client.Close()
		if ctx.Err() != nil {
			return
		}
		m.logger.Printf("adapter %s: %v", m.addr, err)
		m.setState(StateConnecting, err)
		if !sleep(ctx, m.opts.RetryInterval) {
			return
		}
	}
}

// session runs on one adapter connection until it fails.
func (m *Monitor) session(ctx context.Context) error {
	for ctx.Err() == nil {
		m.setState(StateSearching, nil)
		pids, err := m.client.SupportedPIDs()
		if err != nil {
			if !m.client.Connected() {
				return err
			}
			// The adapter works and the car does not answer. Asking too often
			// would keep the car's modules awake, so wait between looks.
			m.setState(StateNoVehicle, err)
			if !sleep(ctx, m.opts.ProbeInterval) {
				return ctx.Err()
			}
			continue
		}
		if err := m.online(ctx, pids); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// online polls until the vehicle goes away (nil) or the connection fails.
func (m *Monitor) online(ctx context.Context, pids []byte) error {
	m.mu.Lock()
	m.supported = pids
	m.version++
	m.mu.Unlock()

	if info, err := m.client.AdapterInfo(); err == nil {
		m.mu.Lock()
		m.adapter = info
		m.version++
		m.mu.Unlock()
	} else if !m.client.Connected() {
		return err
	}
	if _, err := m.RefreshVehicle(); err != nil && !m.client.Connected() {
		return err
	}
	if err := m.RefreshDiagnostics(); err != nil && !m.client.Connected() {
		return err
	}
	m.setState(StateOnline, nil)

	fast, slow := pollPlan(pids)
	failures := map[byte]int{}
	slowNext, lost := 0, 0
	lastDiagnostics := time.Now()
	ticker := time.NewTicker(m.opts.Interval)
	defer ticker.Stop()
	for {
		batch := slices.Clone(fast)
		for i := 0; i < slowPerTick && len(slow) > 0; i++ {
			batch = append(batch, slow[slowNext%len(slow)])
			slowNext++
		}
		read, failed := 0, 0
		for _, pid := range batch {
			if failures[pid] >= maxPIDFailures {
				continue
			}
			reading, err := m.client.ReadPID(pid)
			if err != nil {
				if !m.client.Connected() {
					return err
				}
				failures[pid]++
				failed++
				continue
			}
			failures[pid] = 0
			read++
			m.store(pid, reading)
		}
		m.mu.Lock()
		m.status.Polls++
		m.status.Errors += uint64(failed)
		m.version++
		m.mu.Unlock()

		// Count a tick with nothing read even when every PID has been taken out of
		// polling, or a vehicle that went away would look online forever.
		if read == 0 {
			lost++
			if lost >= lostAfterTicks {
				m.logger.Printf("vehicle stopped answering")
				return nil
			}
		} else {
			lost = 0
		}

		if time.Since(lastDiagnostics) >= m.opts.DiagnosticsInterval {
			lastDiagnostics = time.Now()
			if err := m.RefreshDiagnostics(); err != nil && !m.client.Connected() {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// pollPlan splits the supported PIDs into those read every tick and the rest.
// Range bitmaps are not values.
func pollPlan(pids []byte) (fast, slow []byte) {
	for _, pid := range pids {
		switch {
		case obd.IsBitmap(pid):
		case slices.Contains(fastPIDs, pid):
			fast = append(fast, pid)
		default:
			slow = append(slow, pid)
		}
	}
	return fast, slow
}

func (m *Monitor) store(pid byte, reading obd.Reading) {
	metric := Metric{Reading: reading, Updated: time.Now()}
	if d, ok := obd.Lookup(pid); ok {
		metric.Min, metric.Max = d.Min, d.Max
	}
	m.mu.Lock()
	m.metrics[pid] = metric
	m.status.LastUpdate = metric.Updated
	m.version++
	m.mu.Unlock()
}

// RefreshDiagnostics reads the MIL, the readiness monitors and the trouble
// codes. On a failure the previous values stay.
func (m *Monitor) RefreshDiagnostics() error {
	status, err := m.client.MonitorStatus()
	var monitor *obd.MonitorStatus
	if err == nil {
		monitor = &status
	} else if !elm327.IsNotAvailable(err) {
		return err
	}
	stored, err := m.client.ReadDTCs(obd.DTCStored)
	if err != nil {
		return err
	}
	pending, err := m.client.ReadDTCs(obd.DTCPending)
	if err != nil {
		return err
	}
	permanent, err := m.client.ReadDTCs(obd.DTCPermanent)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.diagnostics = Diagnostics{Monitor: monitor, Stored: stored, Pending: pending, Permanent: permanent, Updated: time.Now()}
	m.version++
	m.mu.Unlock()
	return nil
}

// RefreshVehicle reads the Mode 09 identification.
func (m *Monitor) RefreshVehicle() (obd.VehicleInfo, error) {
	info, err := m.client.VehicleInfo()
	if err != nil {
		return info, err
	}
	m.mu.Lock()
	m.vehicle = info
	m.version++
	m.mu.Unlock()
	return info, nil
}

// ClearDTCs sends Mode 04 and rereads the fault state.
func (m *Monitor) ClearDTCs() error {
	if err := m.client.ClearDTCs(); err != nil {
		return err
	}
	if err := m.RefreshDiagnostics(); err != nil {
		return fmt.Errorf("codes cleared, but rereading them failed: %w", err)
	}
	return nil
}

func (m *Monitor) setState(state State, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.State = state
	m.status.Error = ""
	if err != nil {
		m.status.Error = err.Error()
	}
	m.version++
}

// Snapshot returns a copy of the live state.
func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap := Snapshot{
		Version:     m.version,
		Status:      m.status,
		Adapter:     m.adapter,
		Vehicle:     m.vehicle,
		Supported:   []string{},
		Metrics:     make([]Metric, 0, len(m.metrics)),
		Diagnostics: m.diagnostics,
	}
	for _, pid := range m.supported {
		if !obd.IsBitmap(pid) {
			snap.Supported = append(snap.Supported, fmt.Sprintf("%02X", pid))
		}
	}
	for _, metric := range m.metrics {
		snap.Metrics = append(snap.Metrics, metric)
	}
	slices.SortFunc(snap.Metrics, func(a, b Metric) int {
		switch {
		case a.PID < b.PID:
			return -1
		case a.PID > b.PID:
			return 1
		}
		return 0
	})
	return snap
}

// supportedPIDs returns the Mode 01 PIDs the vehicle advertises.
func (m *Monitor) supportedPIDs() []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.supported)
}

// trace records an exchange. The client calls it after every command.
func (m *Monitor) trace(e elm327.Exchange) {
	entry := Entry{
		Time:       time.Now(),
		Command:    e.Command,
		Response:   e.Response,
		DurationMS: float64(e.Duration.Microseconds()) / 1000,
	}
	if e.Err != nil {
		entry.Error = e.Err.Error()
	}
	m.mu.Lock()
	m.seq++
	entry.Seq = m.seq
	if len(m.entries) < logSize {
		m.entries = append(m.entries, entry)
	} else {
		m.entries[m.next] = entry
	}
	m.next = (m.next + 1) % logSize
	subs := make([]chan Entry, 0, len(m.subs))
	for ch := range m.subs {
		subs = append(subs, ch)
	}
	m.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- entry:
		default: // a slow subscriber misses entries rather than blocking polling
		}
	}
}

// Log returns up to limit recent exchanges after seq, oldest first.
func (m *Monitor) Log(after uint64, limit int) []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := len(m.entries)
	start := 0
	if n == logSize {
		start = m.next
	}
	out := []Entry{}
	for i := range n {
		e := m.entries[(start+i)%n]
		if e.Seq > after {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Subscribe returns a channel of every exchange from now on, and a function
// that ends the subscription.
func (m *Monitor) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 64)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
