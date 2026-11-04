package api

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lsongdev/elm327-go/elm327"
	"github.com/lsongdev/elm327-go/obd"
)

const maxBodySize = 64 << 10

// Options configures the HTTP handler.
type Options struct {
	// Token, when set, must be sent as "Authorization: Bearer <token>" on every
	// request. Hosts that authenticate requests themselves leave it empty.
	Token string
	// Interval is how often /events sends a changed state; zero is 500ms.
	Interval time.Duration
	// AllowClear enables POST /dtc/clear, which erases trouble codes, freeze
	// frames and readiness monitors on the vehicle. It is off unless asked for.
	AllowClear bool
}

// Handler serves the API with relative routes ("/state", "/dtc", ...), so it can
// be mounted under any prefix.
type Handler struct {
	monitor *Monitor
	opts    Options
	mux     *http.ServeMux
}

// NewHandler returns the API handler for m.
func NewHandler(m *Monitor, opts Options) *Handler {
	if opts.Interval <= 0 {
		opts.Interval = 500 * time.Millisecond
	}
	h := &Handler{monitor: m, opts: opts, mux: http.NewServeMux()}
	routes := map[string]func(*http.Request) (any, error){
		"GET /state":        h.state,
		"GET /status":       h.status,
		"GET /metrics":      h.metrics,
		"GET /pids":         h.pids,
		"GET /pids/{pid}":   h.readPID,
		"GET /dtc":          h.dtc,
		"POST /dtc/clear":   h.clearDTC,
		"GET /freeze-frame": h.freezeFrame,
		"GET /vehicle":      h.vehicle,
		"GET /log":          h.log,
		"POST /command":     h.command,
		"POST /decode":      h.decode,
	}
	for pattern, fn := range routes {
		h.mux.Handle(pattern, endpoint(fn))
	}
	h.mux.HandleFunc("GET /events", h.events)
	h.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, errorBody{Error: "no such endpoint"})
	})
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="elm327-go"`)
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "unauthorized"})
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) authorized(r *http.Request) bool {
	if h.opts.Token == "" {
		return true
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(h.opts.Token)) == 1
}

type errorBody struct {
	Error string `json:"error"`
}

type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &statusError{http.StatusBadRequest, fmt.Sprintf(format, args...)}
}

// httpStatus maps client errors onto responses: 503 when no adapter is
// connected, 404 when the vehicle does not have the value, and 502 when the
// adapter or an ECU reports a failure.
func httpStatus(err error) int {
	var known *statusError
	var adapter *elm327.AdapterError
	var refused *elm327.NegativeResponseError
	switch {
	case errors.As(err, &known):
		return known.status
	case errors.Is(err, elm327.ErrNotConnected):
		return http.StatusServiceUnavailable
	case errors.Is(err, elm327.ErrNoData):
		return http.StatusNotFound
	case errors.As(err, &adapter), errors.As(err, &refused):
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}

// endpoint adapts a handler returning (value, error) to JSON responses.
func endpoint(fn func(*http.Request) (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		v, err := fn(r)
		if err != nil {
			writeJSON(w, httpStatus(err), errorBody{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func (h *Handler) snapshot() Snapshot {
	snap := h.monitor.Snapshot()
	snap.Status.ClearEnabled = h.opts.AllowClear
	return snap
}

func (h *Handler) state(*http.Request) (any, error) { return h.snapshot(), nil }

func (h *Handler) status(*http.Request) (any, error) { return h.snapshot().Status, nil }

func (h *Handler) metrics(*http.Request) (any, error) { return h.snapshot().Metrics, nil }

// PIDInfo describes a Mode 01 PID and whether the vehicle advertises it.
type PIDInfo struct {
	PID       string    `json:"pid"`
	Key       string    `json:"key,omitempty"`
	Name      string    `json:"name"`
	Unit      string    `json:"unit,omitempty"`
	Group     obd.Group `json:"group,omitempty"`
	Size      int       `json:"size,omitempty"`
	Min       float64   `json:"min"`
	Max       float64   `json:"max"`
	Supported bool      `json:"supported"`
}

// pids lists every PID the library decodes and any others the vehicle advertises.
func (h *Handler) pids(*http.Request) (any, error) {
	supported := h.monitor.supportedPIDs()
	out := []PIDInfo{}
	for _, d := range obd.Definitions() {
		out = append(out, PIDInfo{
			PID: fmt.Sprintf("%02X", d.PID), Key: d.Key, Name: d.Name, Unit: d.Unit, Group: d.Group,
			Size: d.Size, Min: d.Min, Max: d.Max, Supported: slices.Contains(supported, d.PID),
		})
	}
	for _, pid := range supported {
		if _, known := obd.Lookup(pid); !known && !obd.IsBitmap(pid) {
			out = append(out, PIDInfo{PID: fmt.Sprintf("%02X", pid), Name: fmt.Sprintf("PID %02X", pid), Supported: true})
		}
	}
	slices.SortFunc(out, func(a, b PIDInfo) int { return strings.Compare(a.PID, b.PID) })
	return out, nil
}

// readPID reads one PID now, from every ECU that answers.
func (h *Handler) readPID(r *http.Request) (any, error) {
	value, err := strconv.ParseUint(r.PathValue("pid"), 16, 8)
	if err != nil || len(r.PathValue("pid")) != 2 {
		return nil, badRequest("pid must be two hex digits, like 0C")
	}
	return h.monitor.Client().ReadPIDAll(byte(value))
}

func wantRefresh(r *http.Request) bool {
	v := r.URL.Query().Get("refresh")
	return v != "" && v != "0" && v != "false"
}

// dtc returns the fault state from the last poll, or reads it now with ?refresh=1.
func (h *Handler) dtc(r *http.Request) (any, error) {
	if wantRefresh(r) {
		if err := h.monitor.RefreshDiagnostics(); err != nil {
			return nil, err
		}
	}
	return h.snapshot().Diagnostics, nil
}

type clearRequest struct {
	Confirm bool `json:"confirm"`
}

func (h *Handler) clearDTC(r *http.Request) (any, error) {
	if !h.opts.AllowClear {
		return nil, &statusError{http.StatusForbidden, "clearing trouble codes is disabled; start the server with -allow-clear"}
	}
	var req clearRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, badRequest("invalid JSON body: %v", err)
	}
	if !req.Confirm {
		return nil, badRequest(`clearing erases codes, freeze frames and readiness; send {"confirm": true}`)
	}
	if err := h.monitor.ClearDTCs(); err != nil {
		return nil, err
	}
	return h.snapshot().Diagnostics, nil
}

// freezeFrame reads Mode 02 now. It answers null when no frame is stored.
func (h *Handler) freezeFrame(*http.Request) (any, error) {
	return h.monitor.Client().FreezeFrame()
}

// vehicle returns the identification read on connecting, or reads it now with
// ?refresh=1.
func (h *Handler) vehicle(r *http.Request) (any, error) {
	if wantRefresh(r) {
		return h.monitor.RefreshVehicle()
	}
	return h.snapshot().Vehicle, nil
}

// log lists recent exchanges with the adapter: ?after=<seq>&limit=<n>.
func (h *Handler) log(r *http.Request) (any, error) {
	q := r.URL.Query()
	after, err := uintParam(q.Get("after"))
	if err != nil {
		return nil, badRequest("after: %v", err)
	}
	limit, err := uintParam(q.Get("limit"))
	if err != nil {
		return nil, badRequest("limit: %v", err)
	}
	if limit == 0 {
		limit = 100
	}
	return h.monitor.Log(after, int(limit)), nil
}

func uintParam(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseUint(s, 10, 64)
}

type commandRequest struct {
	Command string `json:"command"`
}

// readOnlyAT are adapter commands that report state without changing it. Others
// would break the monitor's assumptions about the adapter's formatting.
var readOnlyAT = []string{"ATI", "AT@1", "AT@2", "ATDP", "ATDPN", "ATRV", "ATCS", "ATPPS"}

// readOnlyServices are the OBD services that only read. Service 04 clears codes,
// and those above 0A include vendor services that can move actuators or reprogram
// modules, so they never pass through here.
var readOnlyServices = []byte{0x01, 0x02, 0x03, 0x06, 0x07, 0x09, 0x0A}

// checkCommand allows only commands that cannot change the vehicle or adapter.
func checkCommand(command string) (string, error) {
	normalized := strings.ToUpper(strings.NewReplacer(" ", "", "\t", "").Replace(command))
	if normalized == "" {
		return "", badRequest("command is required")
	}
	if strings.HasPrefix(normalized, "AT") {
		if !slices.Contains(readOnlyAT, normalized) {
			return "", &statusError{http.StatusForbidden, fmt.Sprintf("only read-only adapter commands are allowed: %s", strings.Join(readOnlyAT, ", "))}
		}
		return normalized, nil
	}
	b, err := hex.DecodeString(normalized)
	if err != nil || len(b) == 0 || len(b) > 7 {
		return "", badRequest("command must be an AT command or hex digits, like 010C")
	}
	if !slices.Contains(readOnlyServices, b[0]) {
		return "", &statusError{http.StatusForbidden, fmt.Sprintf("service %02X is not allowed; only read-only services are", b[0])}
	}
	return normalized, nil
}

// MessageView is a parsed message with its bytes in hex.
type MessageView struct {
	ECU  string `json:"ecu,omitempty"`
	Data string `json:"data"`
}

// Decoded is what a raw adapter response means.
type Decoded struct {
	Messages []MessageView       `json:"messages"`
	Readings []elm327.ECUReading `json:"readings,omitempty"`
	DTCs     []obd.DTC           `json:"dtcs,omitempty"`
	Error    string              `json:"error,omitempty"` // why it could not be parsed
}

// decodeResponse interprets adapter output from the service bytes it contains.
func decodeResponse(request, raw string) Decoded {
	out := Decoded{Messages: []MessageView{}}
	messages, err := elm327.ParseMessages(request, raw)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	for _, m := range messages {
		out.Messages = append(out.Messages, MessageView{ECU: m.ECU, Data: strings.ToUpper(hex.EncodeToString(m.Data))})
		offset := 0
		switch m.Data[0] {
		case 0x41:
			offset = 2
		case 0x42:
			offset = 3
		case 0x43, 0x47, 0x4A:
			for _, code := range obd.ParseDTCs(m.Data) {
				dtc := obd.NewDTC(code)
				dtc.ECU = m.ECU
				out.DTCs = append(out.DTCs, dtc)
			}
			continue
		default:
			continue
		}
		if len(m.Data) <= offset || obd.IsBitmap(m.Data[1]) {
			continue
		}
		if reading, err := obd.Decode(m.Data[1], m.Data[offset:]); err == nil {
			out.Readings = append(out.Readings, elm327.ECUReading{ECU: m.ECU, Reading: reading})
		} else {
			out.Error = err.Error()
		}
	}
	return out
}

type commandResult struct {
	Command  string `json:"command"`
	Response string `json:"response"`
	Decoded
}

// command sends one read-only command to the adapter and decodes the answer.
func (h *Handler) command(r *http.Request) (any, error) {
	var req commandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, badRequest("invalid JSON body: %v", err)
	}
	command, err := checkCommand(req.Command)
	if err != nil {
		return nil, err
	}
	response, err := h.monitor.Client().Call(command)
	if err != nil {
		return nil, err
	}
	result := commandResult{Command: command, Response: response, Decoded: Decoded{Messages: []MessageView{}}}
	if !strings.HasPrefix(command, "AT") {
		result.Decoded = decodeResponse(command, response)
	}
	return result, nil
}

type decodeRequest struct {
	Request  string `json:"request"`
	Response string `json:"response"`
}

// decode interprets captured adapter output without talking to the adapter.
func (h *Handler) decode(r *http.Request) (any, error) {
	var req decodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, badRequest("invalid JSON body: %v", err)
	}
	if strings.TrimSpace(req.Response) == "" {
		return nil, badRequest("response is required")
	}
	return decodeResponse(req.Request, req.Response), nil
}

// events streams server-sent events: "state" with the whole snapshot when it
// changes (at most every Interval), and with ?log=1 a "log" event per exchange.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "streaming unsupported"})
		return
	}
	var entries <-chan Entry
	if v := r.URL.Query().Get("log"); v != "" && v != "0" && v != "false" {
		ch, cancel := h.monitor.Subscribe()
		defer cancel()
		entries = ch
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		return err
	}
	var sent uint64
	sendState := func() error {
		snap := h.snapshot()
		if snap.Version == sent {
			return nil
		}
		sent = snap.Version
		return send("state", snap)
	}
	if err := sendState(); err != nil {
		return
	}
	flusher.Flush()

	tick := time.NewTicker(h.opts.Interval)
	defer tick.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			err = sendState()
		case <-keepalive.C:
			_, err = fmt.Fprint(w, ": keepalive\n\n")
		case entry := <-entries:
			err = send("log", entry)
		}
		if err != nil {
			return
		}
		flusher.Flush()
	}
}
