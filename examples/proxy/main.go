package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type multiWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (m *multiWriter) Printf(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fmt.Fprintf(m.w, format, args...)
}

func main() {
	var listenAddr string
	var upstreamAddr string
	var outFile string

	flag.StringVar(&listenAddr, "listen", "0.0.0.0:35000", "listen address for app connections")
	flag.StringVar(&upstreamAddr, "upstream", "192.168.3.178:35000", "upstream ELM327 address")
	flag.StringVar(&outFile, "out", "", "optional capture output file")
	flag.Parse()

	var out io.Writer = os.Stdout
	var f *os.File
	if strings.TrimSpace(outFile) != "" {
		var err error
		f, err = os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("open out file: %v", err)
		}
		defer f.Close()
		out = io.MultiWriter(os.Stdout, f)
	}
	logger := &multiWriter{w: out}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", listenAddr, err)
	}
	defer ln.Close()

	log.Printf("proxy listening on %s (upstream %s)", listenAddr, upstreamAddr)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleConn(c, upstreamAddr, logger)
	}
}

func handleConn(appConn net.Conn, upstream string, logger *multiWriter) {
	defer appConn.Close()

	upConn, err := net.DialTimeout("tcp", upstream, 10*time.Second)
	if err != nil {
		log.Printf("dial upstream %s: %v", upstream, err)
		return
	}
	defer upConn.Close()

	id := time.Now().UnixNano()
	log.Printf("client connected id=%d from=%s", id, appConn.RemoteAddr())
	logger.Printf("\n==== CONNECT id=%d time=%s from=%s to=%s ====\n",
		id, time.Now().Format(time.RFC3339Nano), appConn.RemoteAddr(), upstream,
	)

	// Tee both directions for logging while forwarding raw bytes.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		forwardWithCapture(id, "APP → ELM", appConn, upConn, logger)
		_ = upConn.Close()
	}()
	go func() {
		defer wg.Done()
		forwardWithCapture(id, "ELM → APP", upConn, appConn, logger)
		_ = appConn.Close()
	}()

	wg.Wait()
	logger.Printf("==== DISCONNECT id=%d time=%s ====\n", id, time.Now().Format(time.RFC3339Nano))
	log.Printf("client disconnected id=%d", id)
}

func forwardWithCapture(id int64, dir string, src net.Conn, dst net.Conn, logger *multiWriter) {
	// We forward bytes as-is, but also parse "frames" for human readable logs.
	// ELM style:
	// - commands: terminated by CR/LF
	// - responses: may contain CR/LF and end with '>' prompt
	r := bufio.NewReader(src)

	var buf []byte
	flush := func(reason string) {
		if len(buf) == 0 {
			return
		}
		ts := time.Now().Format(time.RFC3339Nano)
		// Make control chars visible.
		s := string(buf)
		s = strings.ReplaceAll(s, "\r", "\\r")
		s = strings.ReplaceAll(s, "\n", "\\n")
		s = strings.ReplaceAll(s, "\t", "\\t")
		logger.Printf("[%s] id=%d %s (%s): %s\n", ts, id, dir, reason, s)
		buf = buf[:0]
	}

	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			// Forward immediately.
			if _, werr := dst.Write(tmp[:n]); werr != nil {
				flush("dst write error")
				return
			}

			// Capture framing.
			for _, b := range tmp[:n] {
				buf = append(buf, b)
				if b == '\r' || b == '\n' {
					flush("line")
				} else if b == '>' {
					flush("prompt")
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				flush("eof")
				return
			}
			flush("read error")
			return
		}
	}
}
