package codex

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

func TestUnixWebSocketAdaptsMessagesAndMasksWrites(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	w := &unixWebSocket{conn: client, reader: bufio.NewReader(client)}
	done := make(chan error, 1)
	go func() { _, e := w.Write([]byte("{\"id\":1}\n")); done <- e }()
	var h [2]byte
	io.ReadFull(server, h[:])
	if h[0] != 129 || h[1]&128 == 0 {
		t.Fatalf("unmasked client frame: %v", h)
	}
	mask := make([]byte, 4)
	io.ReadFull(server, mask)
	payload := make([]byte, int(h[1]&127))
	io.ReadFull(server, payload)
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	if string(payload) != `{"id":1}` {
		t.Fatal(string(payload))
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	go func() { server.Write([]byte{0x01, 3, 'a', 'b', 'c', 0x80, 2, 'd', 'e'}) }()
	line, e := bufio.NewReader(w).ReadString('\n')
	if e != nil || line != "abcde\n" {
		t.Fatalf("fragmented read %q %v", line, e)
	}
}
func TestUnixWebSocketRejectsOversizedFrame(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	w := &unixWebSocket{conn: client, reader: bufio.NewReader(client)}
	go func() {
		h := make([]byte, 10)
		h[0] = 129
		h[1] = 127
		binary.BigEndian.PutUint64(h[2:], 17<<20)
		server.Write(h)
	}()
	if _, e := w.Read(make([]byte, 1)); e == nil {
		t.Fatal("oversized frame accepted")
	}
}
