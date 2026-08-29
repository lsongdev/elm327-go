package elm327

import (
	"encoding/hex"
	"fmt"
	"strings"
)

var adapterErrorResponses = map[string]struct{}{
	"?":                 {},
	"BUS ERROR":         {},
	"BUS INIT: ERROR":   {},
	"CAN ERROR":         {},
	"DATA ERROR":        {},
	"ERROR":             {},
	"FB ERROR":          {},
	"LV RESET":          {},
	"STOPPED":           {},
	"UNABLE TO CONNECT": {},
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

func normalizeMode01PID(pid string) (string, error) {
	pid = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(pid), " ", ""))
	if len(pid) != 4 || !strings.HasPrefix(pid, "01") || !isHex(pid) {
		return "", fmt.Errorf("invalid Mode 01 PID %q", pid)
	}
	return pid, nil
}

func extractMode01DataBytes(pid, rawResp string) ([]byte, error) {
	responses, err := extractMode01Responses(pid, rawResp)
	if err != nil {
		return nil, err
	}
	return responses[0], nil
}

func extractMode01Responses(pid, rawResp string) ([][]byte, error) {
	pid, err := normalizeMode01PID(pid)
	if err != nil {
		return nil, err
	}
	signature := "41" + pid[2:4]
	var responses [][]byte

	for _, rawLine := range logicalOBDLines(rawResp) {
		line := strings.ToUpper(strings.TrimSpace(rawLine))
		compact := strings.ReplaceAll(strings.ReplaceAll(line, " ", ""), "\t", "")
		if compact == pid || line == "SEARCHING..." {
			continue
		}
		if line == "NO DATA" {
			return nil, fmt.Errorf("no data for pid %s", pid)
		}
		if _, ok := adapterErrorResponses[line]; ok {
			return nil, fmt.Errorf("adapter error for pid %s: %s", pid, line)
		}
		if !isHex(compact) {
			continue
		}
		foundPositive := false

		// The signature may follow a three-nibble 11-bit header or an eight-nibble
		// 29-bit header and a DLC/PCI byte, so both nibble alignments are valid.
		for offset := 0; offset+len(signature) <= len(compact); offset++ {
			if compact[offset:offset+len(signature)] != signature {
				continue
			}
			dataHex := compact[offset+len(signature):]
			if dataHex == "" || len(dataHex)%2 != 0 {
				continue
			}
			data, decodeErr := hex.DecodeString(dataHex)
			if decodeErr != nil {
				return nil, fmt.Errorf("invalid response for pid %s: %q: %w", pid, rawLine, decodeErr)
			}
			responses = append(responses, data)
			foundPositive = true
			break
		}
		if !foundPositive && strings.Contains(compact, "7F01") {
			return nil, fmt.Errorf("negative OBD response for pid %s: %s", pid, rawLine)
		}
	}

	if len(responses) == 0 {
		return nil, fmt.Errorf("unexpected response for pid %s: %q", pid, rawResp)
	}
	return responses, nil
}

// logicalOBDLines reassembles ELM327 numbered multiline output such as
// "0: 49 02 ...", "1: ..." while leaving normal ECU response lines separate.
func logicalOBDLines(raw string) []string {
	physical := strings.Split(normalizeResponse(raw), "\n")
	lines := make([]string, 0, len(physical))
	var multiline strings.Builder
	flush := func() {
		if multiline.Len() > 0 {
			lines = append(lines, multiline.String())
			multiline.Reset()
		}
	}
	for _, line := range physical {
		line = strings.TrimSpace(line)
		if len(line) >= 2 && line[1] == ':' && isHex(line[:1]) {
			if line[0] == '0' && multiline.Len() > 0 {
				flush()
			}
			if multiline.Len() > 0 {
				multiline.WriteByte(' ')
			}
			multiline.WriteString(strings.TrimSpace(line[2:]))
			continue
		}
		flush()
		lines = append(lines, line)
	}
	flush()
	return lines
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
