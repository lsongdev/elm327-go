package elm327

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"
)

const defaultMaxCommandSize = 4 << 10

// HandlerFunc handles one AT or OBD command. The response must not contain the
// final '>' prompt. handled=false delegates to the built-in fallback behavior.
type HandlerFunc func(command string) (response string, handled bool)

// HandlerFactory creates independent ELM327 state for a TCP connection.
type HandlerFactory func() HandlerFunc

// Server is a TCP ELM327-compatible command server.
type Server struct {
	Addr string

	// Handler is retained for stateless handlers. NewHandler takes precedence and
	// should be used whenever commands change per-connection adapter settings.
	Handler    HandlerFunc
	NewHandler HandlerFactory

	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// MaxCommandSize limits a command before CR/LF. Zero means 4 KiB.
	MaxCommandSize int
	Logger         *log.Logger
}

// ListenAndServe starts a server using a stateless/shared handler. Stateful
// implementations should use ListenAndServeSessions.
func ListenAndServe(addr string, handler HandlerFunc) error {
	return (&Server{Addr: addr, Handler: handler}).ListenAndServe()
}

// ListenAndServeSessions starts a server with an independent handler for every
// TCP connection.
func ListenAndServeSessions(addr string, factory HandlerFactory) error {
	return (&Server{Addr: addr, NewHandler: factory}).ListenAndServe()
}

func (s *Server) ListenAndServe() error {
	addr := s.Addr
	if addr == "" {
		addr = ":35000"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	return s.Serve(listener)
}

// Serve accepts connections until listener is closed. Closing listener stops
// accepting new clients.
func (s *Server) Serve(listener net.Listener) error {
	s.logger().Printf("Listening on %s", listener.Addr())
	var delay time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else {
				delay *= 2
			}
			if maxDelay := time.Second; delay > maxDelay {
				delay = maxDelay
			}
			s.logger().Printf("accept error: %v; retrying in %v", err, delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		handler := s.Handler
		if s.NewHandler != nil {
			handler = s.NewHandler()
		}
		go s.handleClient(conn, handler)
	}
}

func (s *Server) handleClient(conn net.Conn, handler HandlerFunc) {
	defer conn.Close()
	s.logger().Printf("Client connected: %s", conn.RemoteAddr())
	if err := s.writeResponse(conn, ""); err != nil {
		return
	}

	reader := &commandReader{r: bufio.NewReader(conn)}
	var lastCommand string
	for {
		if s.ReadTimeout > 0 {
			if err := conn.SetReadDeadline(time.Now().Add(s.ReadTimeout)); err != nil {
				return
			}
		}
		cmd, err := reader.Read(s.maxCommandSize())
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.logger().Printf("read from %s: %v", conn.RemoteAddr(), err)
			}
			return
		}

		command := strings.TrimSpace(cmd)
		repeated := command == ""
		if repeated {
			command = lastCommand
			if command == "" {
				if err := s.writeResponse(conn, ""); err != nil {
					return
				}
				continue
			}
		} else {
			lastCommand = command
		}

		s.logger().Printf("[RX %s] %s", conn.RemoteAddr(), command)
		response, handled := "", false
		if handler != nil {
			response, handled = handler(command)
		}
		if !handled {
			response = defaultResponse(command)
		}
		if repeated {
			response = stripRepeatedEcho(response, command)
		}
		if err := s.writeResponse(conn, response); err != nil {
			return
		}
		s.logger().Printf("[TX %s] %s", conn.RemoteAddr(), strings.TrimSpace(response))
	}
}

func (s *Server) writeResponse(conn net.Conn, response string) error {
	if s.WriteTimeout > 0 {
		if err := conn.SetWriteDeadline(time.Now().Add(s.WriteTimeout)); err != nil {
			return err
		}
	}
	if response != "" {
		if err := writeFull(conn, []byte(response)); err != nil {
			return err
		}
		if !strings.HasSuffix(response, "\r") && !strings.HasSuffix(response, "\n") {
			if err := writeFull(conn, []byte("\r")); err != nil {
				return err
			}
		}
	}
	return writeFull(conn, []byte(">"))
}

func (s *Server) maxCommandSize() int {
	if s.MaxCommandSize > 0 {
		return s.MaxCommandSize
	}
	return defaultMaxCommandSize
}

func (s *Server) logger() *log.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return log.Default()
}

func defaultResponse(command string) string {
	normalized := normalizeCommand(command)
	switch {
	case normalized == "?":
		return "?"
	case normalized == "ATZ", normalized == "ATI":
		return "ELM327 v1.5"
	case strings.HasPrefix(normalized, "AT"):
		return "?"
	case len(normalized) >= 4:
		return "NO DATA"
	default:
		return "?"
	}
}

func normalizeCommand(command string) string {
	command = strings.ToUpper(strings.TrimSpace(command))
	command = strings.ReplaceAll(command, " ", "")
	command = strings.ReplaceAll(command, "\t", "")
	return command
}

type commandReader struct {
	r            *bufio.Reader
	previousTerm byte
}

func (r *commandReader) Read(maxSize int) (string, error) {
	var b strings.Builder
	for {
		ch, err := r.r.ReadByte()
		if err != nil {
			return "", err
		}
		if ch == '\r' || ch == '\n' {
			if b.Len() == 0 && ((r.previousTerm == '\r' && ch == '\n') || (r.previousTerm == '\n' && ch == '\r')) {
				r.previousTerm = 0
				continue
			}
			r.previousTerm = ch
			return b.String(), nil
		}
		r.previousTerm = 0
		if b.Len() >= maxSize {
			return "", fmt.Errorf("command exceeds %d bytes", maxSize)
		}
		b.WriteByte(ch)
	}
}

func readCommand(r *bufio.Reader, maxSize int) (string, error) {
	return (&commandReader{r: r}).Read(maxSize)
}

func stripRepeatedEcho(response, command string) string {
	for _, candidate := range []string{command, normalizeCommand(command)} {
		for _, sep := range []string{"\r\n", "\r", "\n"} {
			prefix := candidate + sep
			if len(response) >= len(prefix) && strings.EqualFold(response[:len(prefix)], prefix) {
				return response[len(prefix):]
			}
		}
	}
	return response
}
