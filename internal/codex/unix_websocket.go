package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// unixWebSocket adapts WebSocket messages to the existing newline JSON-RPC
// transport. Core connects directly to App Server; there is no RPC broker.
type unixWebSocket struct {
	conn    net.Conn
	reader  *bufio.Reader
	pending []byte
	mu      sync.Mutex
}

func dialUnixWebSocket(ctx context.Context, path string) (*unixWebSocket, error) {
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*unixWebSocket, error) { c.Close(); return nil, e }
	nonce := make([]byte, 16)
	rand.Read(nonce)
	key := base64.StdEncoding.EncodeToString(nonce)
	deadline := time.Now().Add(10 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	c.SetDeadline(deadline)
	stopCancel := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopCancel()
	_, e = fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key)
	if e != nil {
		return fail(e)
	}
	r := bufio.NewReader(c)
	response, e := http.ReadResponse(r, &http.Request{Method: "GET"})
	if e != nil {
		return fail(e)
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if response.StatusCode != 101 || response.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		return fail(errors.New("invalid WebSocket upgrade"))
	}
	c.SetDeadline(time.Time{})
	return &unixWebSocket{conn: c, reader: r}, nil
}
func (w *unixWebSocket) Close() error { return w.conn.Close() }
func (w *unixWebSocket) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, line := range bytes.Split(bytes.TrimSpace(p), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if e := w.frame(1, line); e != nil {
			return 0, e
		}
	}
	return len(p), nil
}
func (w *unixWebSocket) frame(op byte, p []byte) error {
	h := []byte{0x80 | op}
	n := len(p)
	if n < 126 {
		h = append(h, 0x80|byte(n))
	} else if n <= 65535 {
		h = append(h, 0x80|126, byte(n>>8), byte(n))
	} else {
		h = append(h, 0x80|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		h = append(h, b[:]...)
	}
	var mask [4]byte
	rand.Read(mask[:])
	h = append(h, mask[:]...)
	data := make([]byte, n)
	for i := range p {
		data[i] = p[i] ^ mask[i%4]
	}
	h = append(h, data...)
	for len(h) > 0 {
		n, e := w.conn.Write(h)
		if e != nil {
			return e
		}
		h = h[n:]
	}
	return nil
}
func (w *unixWebSocket) Read(p []byte) (int, error) {
	for len(w.pending) == 0 {
		var message []byte
		for {
			var h [2]byte
			if _, e := io.ReadFull(w.reader, h[:]); e != nil {
				return 0, e
			}
			op := h[0] & 15
			if h[0]&0x70 != 0 || (op >= 8 && (h[0]&128 == 0 || h[1]&127 > 125)) {
				return 0, errors.New("invalid WebSocket control frame")
			}
			n := uint64(h[1] & 127)
			if n == 126 {
				var b [2]byte
				if _, e := io.ReadFull(w.reader, b[:]); e != nil {
					return 0, e
				}
				n = uint64(binary.BigEndian.Uint16(b[:]))
			} else if n == 127 {
				var b [8]byte
				if _, e := io.ReadFull(w.reader, b[:]); e != nil {
					return 0, e
				}
				n = binary.BigEndian.Uint64(b[:])
			}
			if n > 16<<20 || h[1]&128 != 0 {
				return 0, errors.New("invalid server WebSocket frame")
			}
			data := make([]byte, int(n))
			if _, e := io.ReadFull(w.reader, data); e != nil {
				return 0, e
			}
			if op == 8 {
				return 0, io.EOF
			}
			if op == 9 {
				w.mu.Lock()
				e := w.frame(10, data)
				w.mu.Unlock()
				if e != nil {
					return 0, e
				}
				continue
			}
			if op == 10 {
				continue
			}
			if op != 0 && op != 1 {
				return 0, errors.New("unsupported WebSocket message")
			}
			message = append(message, data...)
			if len(message) > 16<<20 {
				return 0, errors.New("WebSocket message too large")
			}
			if h[0]&128 != 0 {
				break
			}
		}
		w.pending = append(message, '\n')
	}
	n := copy(p, w.pending)
	w.pending = w.pending[n:]
	return n, nil
}
