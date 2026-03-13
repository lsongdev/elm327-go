package elm327

import (
	"bufio"
	"log"
	"net"
	"strings"
)

type Handler interface {
	// HandleAT should return (response, true) if handled.
	HandleAT(command string) (string, bool)
	// HandlePID should return (response, true) if handled.
	HandlePID(command string) (string, bool)
}

func ListenAndServe(addr string, h Handler) (err error) {
	log.Println("Listening on", addr)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		go handleClient(conn, h)
	}
}

func handleClient(conn net.Conn, h Handler) {
	defer conn.Close()
	log.Println("Client connected")

	// Many ELM327 adapters present a prompt immediately after connect.
	_, _ = conn.Write([]byte(">"))

	reader := bufio.NewReader(conn)
	for {
		cmd, err := readCommand(reader)
		if err != nil {
			return
		}

		command := strings.TrimSpace(cmd)
		if command == "" {
			continue
		}

		log.Printf("[RX] %s", command)

		var response string
		handled := false

		upper := strings.ToUpper(command)
		normalized := normalizeCommand(upper)

		if h != nil && (strings.HasPrefix(normalized, "AT") || strings.HasPrefix(normalized, "ST") || strings.HasPrefix(normalized, "VT")) {
			if r, ok := h.HandleAT(normalized); ok {
				response, handled = r, true
			}
		} else if h != nil && len(normalized) >= 4 { // OBD2 PID (e.g. 010C)
			if r, ok := h.HandlePID(normalized); ok {
				response, handled = r, true
			}
		}

		if !handled {
			// Reasonable defaults.
			if normalized == "?" {
				response = "?"
			} else if strings.HasPrefix(normalized, "AT") {
				response = defaultAT(normalized)
			} else if len(normalized) >= 4 {
				response = "NO DATA"
			} else {
				// ELM327 typically replies '?' for unknown short commands.
				response = "?"
			}
		}

		if response != "" {
			response += "\r>"
			conn.Write([]byte(response))
			log.Printf("[TX] %s", strings.TrimSpace(response))
		}
	}
}

func defaultAT(command string) string {
	command = normalizeCommand(command)
	switch command {
	case "ATZ":
		return "ELM327 v1.5"
	default:
		if strings.HasPrefix(command, "AT") {
			return "OK"
		}
		return ""
	}
}

func normalizeCommand(command string) string {
	// Remove whitespace to accept both "AT Z" and "ATZ".
	command = strings.ToUpper(strings.TrimSpace(command))
	command = strings.ReplaceAll(command, " ", "")
	command = strings.ReplaceAll(command, "\t", "")
	return command
}

func readCommand(r *bufio.Reader) (string, error) {
	// Commands are terminated by CR or LF. We consume one command at a time.
	var b strings.Builder
	for {
		ch, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		if ch == '\r' || ch == '\n' {
			// Swallow possible \r\n or \n\r combos.
			for {
				next, err := r.Peek(1)
				if err != nil {
					break
				}
				if next[0] == '\r' || next[0] == '\n' {
					_, _ = r.ReadByte()
					continue
				}
				break
			}
			return b.String(), nil
		}
		b.WriteByte(ch)
	}
}
