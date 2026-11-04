package elm327

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrNoData means the adapter asked the vehicle and nothing answered: the PID
// is unsupported, or the ignition is off.
var ErrNoData = errors.New("no data")

// AdapterError is a status the ELM327 printed instead of data, such as
// "UNABLE TO CONNECT" or "CAN ERROR". The connection stays usable.
type AdapterError struct {
	Status string
}

func (e *AdapterError) Error() string { return "adapter error: " + e.Status }

// NegativeResponseError is an ECU's refusal of a request (service 7F).
type NegativeResponseError struct {
	ECU     string
	Service byte
	Code    byte // ISO 14229 negative response code
}

func (e *NegativeResponseError) Error() string {
	text, ok := negativeResponses[e.Code]
	if !ok {
		text = "unknown reason"
	}
	return fmt.Sprintf("ECU refused service %02X: %s (NRC %02X)", e.Service, text, e.Code)
}

var negativeResponses = map[byte]string{
	0x10: "general reject",
	0x11: "service not supported",
	0x12: "sub-function not supported",
	0x13: "incorrect message length",
	0x21: "busy, repeat request",
	0x22: "conditions not correct",
	0x31: "request out of range",
	0x33: "security access denied",
}

const negativeResponsePending = 0x78

// Message is one complete response from one ECU: the service byte (41, 43, ...)
// and everything after it, with headers, PCI bytes, padding and checksums removed.
type Message struct {
	ECU  string // response address such as "7E8", or "" when headers are off
	Data []byte
}

// adapterStatuses are the non-data lines an ELM327 can print, without spaces
// because some adapters drop them after ATS0.
var adapterStatuses = map[string]struct{}{
	"?":                {},
	"ACTALERT":         {},
	"BUFFERFULL":       {},
	"BUSBUSY":          {},
	"BUSERROR":         {},
	"BUSINIT:ERROR":    {},
	"BUSINIT:...ERROR": {},
	"CANERROR":         {},
	"DATAERROR":        {},
	"ERROR":            {},
	"FBERROR":          {},
	"FCRXTIMEOUT":      {},
	"LOWPOWERALERT":    {},
	"LVRESET":          {},
	"RXERROR":          {},
	"STOPPED":          {},
	"UNABLETOCONNECT":  {},
}

func normalizeResponse(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")
	out := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func compactHex(s string) string {
	return strings.NewReplacer(" ", "", "\t", "").Replace(strings.ToUpper(strings.TrimSpace(s)))
}

// ParseMessages turns the text an ELM327 printed into per-ECU messages, sorted
// by ECU address. It accepts any combination of echo, headers (11-bit, 29-bit
// and legacy), spaces, CAN auto-formatting on or off and multi-frame responses.
// request, when not empty, is removed if the adapter echoed it.
//
// It returns ErrNoData, *AdapterError or *NegativeResponseError when no ECU
// sent usable data. A line that is not understood is skipped.
func ParseMessages(request, raw string) ([]Message, error) {
	p := messageParser{open: map[string]*reassembly{}}
	request = compactHex(request)

	for _, line := range strings.Split(normalizeResponse(raw), "\n") {
		line = strings.TrimSpace(line)
		if upper := strings.ToUpper(line); strings.HasPrefix(upper, "SEARCHING") {
			line = strings.TrimSpace(strings.TrimLeft(line[len("SEARCHING"):], "."))
		}
		compact := compactHex(line)
		if compact == "" || compact == request {
			continue
		}
		if p.status(compact) {
			continue
		}
		if len(compact) >= 2 && compact[1] == ':' && isHex(compact[:1]) {
			p.numbered(compact)
			continue
		}
		p.flushNumbered()
		if !isHex(compact) {
			continue
		}
		if len(compact) == 3 {
			// "014" before numbered lines: the total length of the message.
			fmt.Sscanf(compact, "%X", &p.numberedTotal)
			continue
		}
		p.frame(compact)
	}
	p.flushNumbered()
	return p.result(raw)
}

type reassembly struct {
	total int
	next  byte
	data  []byte
}

type messageParser struct {
	messages []Message
	open     map[string]*reassembly

	numberedData  []byte
	numberedTotal int

	refused    error
	statusErr  error
	incomplete bool
}

// status consumes a line that reports adapter state rather than data.
func (p *messageParser) status(compact string) bool {
	if i := strings.IndexByte(compact, '<'); i >= 0 {
		// "7E8 04 41 0C <RX ERROR": the adapter marks this frame as corrupt.
		p.setStatus(&AdapterError{Status: compact[i+1:]})
		return true
	}
	if compact == "NODATA" {
		if p.statusErr == nil {
			p.statusErr = ErrNoData
		}
		return true
	}
	if strings.HasPrefix(compact, "BUSINIT") && strings.HasSuffix(compact, "OK") {
		return true
	}
	_, known := adapterStatuses[compact]
	if !known && strings.HasPrefix(compact, "ERR") && len(compact) == 5 {
		known = isHex(compact[3:]) // ERR94 and friends
	}
	if known {
		p.setStatus(&AdapterError{Status: compact})
	}
	return known
}

func (p *messageParser) setStatus(err error) {
	if p.statusErr == nil || errors.Is(p.statusErr, ErrNoData) {
		p.statusErr = err
	}
}

// numbered handles "0:4902013147" lines: multi-frame output with headers off.
func (p *messageParser) numbered(compact string) {
	body := compact[2:]
	if compact[0] == '0' {
		total := p.numberedTotal
		p.flushNumbered()
		p.numberedTotal = total
	}
	if decoded, err := hex.DecodeString(body); err == nil {
		p.numberedData = append(p.numberedData, decoded...)
	}
}

func (p *messageParser) flushNumbered() {
	data := p.numberedData
	total := p.numberedTotal
	p.numberedData, p.numberedTotal = nil, 0
	if len(data) == 0 {
		return
	}
	if total > 0 && total < len(data) {
		data = data[:total]
	}
	p.add("", data, false)
}

// isResponseService reports a byte that starts a service response.
func isResponseService(b byte) bool {
	return (b >= 0x41 && b <= 0x4A) || b == 0x7F
}

// frame classifies one line of hex digits by its shape.
func (p *messageParser) frame(compact string) {
	// An odd number of digits can only be a 3-digit 11-bit CAN header.
	if len(compact)%2 == 1 {
		if len(compact) < 5 {
			return
		}
		if body, err := hex.DecodeString(compact[3:]); err == nil {
			p.can(compact[:3], body)
		}
		return
	}
	if len(compact) >= 10 && (strings.HasPrefix(compact, "18DA") || strings.HasPrefix(compact, "18DB")) {
		if body, err := hex.DecodeString(compact[8:]); err == nil {
			p.can(compact[:8], body)
		}
		return
	}
	b, err := hex.DecodeString(compact)
	if err != nil || len(b) == 0 {
		return
	}
	switch {
	case isLegacyHeader(b):
		// 3 header bytes, the third the source, and a trailing checksum.
		p.add(fmt.Sprintf("%02X", b[2]), b[3:len(b)-1], true)
	case isResponseService(b[0]):
		// A 7-byte service 43/49 line is a frame of a legacy message.
		p.add("", b, len(b) == 7 && (b[0] == 0x43 || b[0] == 0x49))
	case b[0] <= 0x2F:
		p.can("", b) // CAN auto-formatting off: the PCI byte is visible
	}
}

// isLegacyHeader recognizes J1850, ISO 9141 and KWP lines with headers on, such
// as "48 6B 10 41 0C 1A F8 CK". 48 is also the response to service 08, so it
// only counts as a format byte when followed by the tester address.
func isLegacyHeader(b []byte) bool {
	// Format bytes start at 48, which also keeps PCI bytes out of this case.
	if len(b) < 5 || b[0] < 0x48 || !isResponseService(b[3]) {
		return false
	}
	if b[0] == 0x48 {
		return b[1] == 0x6B
	}
	return !isResponseService(b[0])
}

// can handles a CAN frame body, starting at its ISO 15765-2 PCI byte.
func (p *messageParser) can(ecu string, body []byte) {
	if len(body) == 0 {
		return
	}
	switch body[0] >> 4 {
	case 0x0: // single frame
		size := int(body[0] & 0x0F)
		if size == 0 || size > len(body)-1 {
			return
		}
		p.add(ecu, body[1:1+size], false)
	case 0x1: // first frame
		if len(body) < 3 {
			return
		}
		total := int(body[0]&0x0F)<<8 | int(body[1])
		p.open[ecu] = &reassembly{total: total, next: 1, data: slices.Clone(body[2:])}
		p.closeIfComplete(ecu)
	case 0x2: // consecutive frame
		r := p.open[ecu]
		if r == nil {
			return
		}
		if body[0]&0x0F != r.next {
			p.incomplete = true
			delete(p.open, ecu) // a lost frame makes the rest meaningless
			return
		}
		r.next = (r.next + 1) & 0x0F
		r.data = append(r.data, body[1:]...)
		p.closeIfComplete(ecu)
	case 0x3: // flow control, sent by the tester side
	default:
		// Some clones keep the header but drop the PCI byte.
		if isResponseService(body[0]) {
			p.add(ecu, body, false)
		}
	}
}

func (p *messageParser) closeIfComplete(ecu string) {
	r := p.open[ecu]
	if r == nil || len(r.data) < r.total {
		return
	}
	delete(p.open, ecu)
	p.add(ecu, r.data[:r.total], false)
}

// add records a finished message. Legacy protocols split long responses into
// frames that each repeat the service; merge those per ECU, keeping the first
// frame's sequence byte where a CAN response has its count byte.
func (p *messageParser) add(ecu string, data []byte, legacy bool) {
	if len(data) == 0 {
		return
	}
	if data[0] == 0x7F {
		if len(data) >= 3 && data[2] == negativeResponsePending {
			return // the real answer follows
		}
		if p.refused == nil && len(data) >= 3 {
			p.refused = &NegativeResponseError{ECU: ecu, Service: data[1], Code: data[2]}
		}
		return
	}
	if legacy {
		for i := range p.messages {
			m := &p.messages[i]
			if m.ECU != ecu || m.Data[0] != data[0] {
				continue
			}
			switch {
			case data[0] == 0x43:
				m.Data = append(m.Data, data[1:]...)
				return
			case data[0] == 0x49 && len(data) > 3 && len(m.Data) > 1 && m.Data[1] == data[1]:
				m.Data = append(m.Data, data[3:]...)
				return
			}
		}
	}
	p.messages = append(p.messages, Message{ECU: ecu, Data: bytes.Clone(data)})
}

func (p *messageParser) result(raw string) ([]Message, error) {
	if len(p.open) > 0 {
		p.incomplete = true
	}
	if len(p.messages) > 0 {
		slices.SortStableFunc(p.messages, func(a, b Message) int { return strings.Compare(a.ECU, b.ECU) })
		return p.messages, nil
	}
	switch {
	case p.refused != nil:
		return nil, p.refused
	case p.statusErr != nil:
		return nil, p.statusErr
	case p.incomplete:
		return nil, fmt.Errorf("incomplete multi-frame response: %q", raw)
	}
	return nil, fmt.Errorf("unexpected response: %q", raw)
}

// parseRequest validates the hex digits of an OBD request.
func parseRequest(request string) (string, []byte, error) {
	normalized := compactHex(request)
	if len(normalized)%2 != 0 || !isHex(normalized) {
		return "", nil, fmt.Errorf("invalid OBD request %q", request)
	}
	b, err := hex.DecodeString(normalized)
	if err != nil {
		return "", nil, fmt.Errorf("invalid OBD request %q: %w", request, err)
	}
	return normalized, b, nil
}

// matchResponse keeps the messages that answer request: the service plus 40,
// and the same PID for the services that echo one.
func matchResponse(request []byte, messages []Message) []Message {
	var out []Message
	for _, m := range messages {
		if m.Data[0] != request[0]+0x40 {
			continue
		}
		switch request[0] {
		case 0x01, 0x02, 0x09:
			if len(request) > 1 && (len(m.Data) < 2 || m.Data[1] != request[1]) {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func normalizeMode01PID(pid string) (string, error) {
	pid = compactHex(pid)
	if len(pid) != 4 || !strings.HasPrefix(pid, "01") || !isHex(pid) {
		return "", fmt.Errorf("invalid Mode 01 PID %q", pid)
	}
	return pid, nil
}

// extractMode01Responses returns the data bytes of each ECU's answer to pid.
func extractMode01Responses(pid, rawResp string) ([][]byte, error) {
	pid, err := normalizeMode01PID(pid)
	if err != nil {
		return nil, err
	}
	_, request, err := parseRequest(pid)
	if err != nil {
		return nil, err
	}
	messages, err := ParseMessages(pid, rawResp)
	if err != nil {
		return nil, fmt.Errorf("pid %s: %w", pid, err)
	}
	var responses [][]byte
	for _, m := range matchResponse(request, messages) {
		if len(m.Data) > 2 {
			responses = append(responses, m.Data[2:])
		}
	}
	if len(responses) == 0 {
		return nil, fmt.Errorf("unexpected response for pid %s: %q", pid, rawResp)
	}
	return responses, nil
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if !((ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'F') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}
