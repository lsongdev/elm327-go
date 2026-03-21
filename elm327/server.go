package elm327

import (
	"bufio"
	"log"
	"net"
	"strings"
)

// HandlerFunc is a function that handles AT and PID commands.
// It should return (response, true) if the command is handled,
// or (response, false) if the command should be handled by default logic.
type HandlerFunc func(command string) (response string, handled bool)

// ListenAndServe starts a TCP server on the given address and handles
// incoming ELM327 commands using the provided handler function.
func ListenAndServe(addr string, handler HandlerFunc) (err error) {
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

		go handleClient(conn, handler)
	}
}

func handleClient(conn net.Conn, handler HandlerFunc) {
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

		if handler != nil {
			response, handled = handler(command)
		}

		if !handled {
			// Reasonable defaults.
			upper := strings.ToUpper(command)
			normalized := normalizeCommand(upper)
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
