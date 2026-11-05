package git

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// All inputs in this file are tiny fixed memory slices. No process, file,
// pipe, socket, repository, or service is opened or invoked by these tests.
type copyContractReaderOnly struct{ io.Reader }
type copyContractWriterOnly struct{ io.Writer }

type copyContractChunkReader struct {
	data     []byte
	chunk    int
	terminal error
}

func (r *copyContractChunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		if r.terminal != nil {
			return 0, r.terminal
		}
		return 0, io.EOF
	}
	n := min(len(p), len(r.data))
	if r.chunk > 0 {
		n = min(n, r.chunk)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	if len(r.data) == 0 && r.terminal != nil {
		return n, r.terminal
	}
	return n, nil
}

func assertCopyContract(t *testing.T, b *limitedBuffer, n int64, err error, input []byte, limit int, wantErr error) {
	t.Helper()
	retained := min(len(input), limit+1)
	wantExceeded := len(input) > limit
	t.Logf("copied=%d retained=%d limit=%d exceeded=%v err=%v; want copied=%d retained=%d exceeded=%v err=%v", n, b.Len(), limit, b.exceeded, err, len(input), retained, wantExceeded, wantErr)
	if n != int64(len(input)) {
		t.Errorf("copied=%d, want %d", n, len(input))
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error=%v, want %v", err, wantErr)
	}
	if b.Len() != retained {
		t.Errorf("retained=%d, want %d", b.Len(), retained)
	}
	if b.exceeded != wantExceeded {
		t.Errorf("exceeded=%v, want %v", b.exceeded, wantExceeded)
	}
	if !bytes.Equal(b.Bytes(), input[:retained]) {
		t.Errorf("retained bytes do not equal the expected prefix")
	}
}

func TestLimitedBufferCopyControls(t *testing.T) {
	payload := []byte(strings.Repeat("abcdefgh", 16)) // 128 bytes maximum.
	t.Run("dispatch_interfaces", func(t *testing.T) {
		if _, ok := any(&limitedBuffer{}).(io.ReaderFrom); !ok {
			t.Fatal("baseline must expose promoted ReaderFrom")
		}
		if _, ok := any(copyContractReaderOnly{bytes.NewReader(payload)}).(io.WriterTo); ok {
			t.Fatal("Reader-only wrapper unexpectedly exposes WriterTo")
		}
		if _, ok := any(copyContractWriterOnly{&limitedBuffer{}}).(io.ReaderFrom); ok {
			t.Fatal("Writer-only wrapper unexpectedly exposes ReaderFrom")
		}
		if _, ok := any(bytes.NewReader(payload)).(io.WriterTo); !ok {
			t.Fatal("bytes.Reader control must expose WriterTo")
		}
		if _, ok := any(bytes.NewBuffer(payload)).(io.WriterTo); !ok {
			t.Fatal("bytes.Buffer control must expose WriterTo")
		}
	})
	for _, tc := range []struct {
		name               string
		size, limit, chunk int
	}{
		{"direct_empty", 0, 32, 0},
		{"direct_below", 31, 32, 0},
		{"direct_exact", 32, 32, 0},
		{"direct_one_over", 33, 32, 0},
		{"direct_over", 128, 32, 0},
		{"direct_chunked", 128, 32, 7},
		{"direct_zero_limit_empty", 0, 0, 0},
		{"direct_zero_limit_one", 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &limitedBuffer{limit: tc.limit}
			input := payload[:tc.size]
			var n int64
			if tc.chunk == 0 {
				count, err := b.Write(input)
				if err != nil {
					t.Fatal(err)
				}
				n = int64(count)
			} else {
				for offset := 0; offset < len(input); {
					end := min(offset+tc.chunk, len(input))
					count, err := b.Write(input[offset:end])
					if err != nil {
						t.Fatal(err)
					}
					n += int64(count)
					offset = end
				}
			}
			assertCopyContract(t, b, n, nil, input, tc.limit, nil)
			before := b.Len()
			if count, err := b.Write(nil); count != 0 || err != nil || b.Len() != before {
				t.Fatal("empty write changed count/error/length")
			}
		})
	}
	sentinel := errors.New("synthetic reader error")
	for _, tc := range []struct {
		name            string
		size            int
		factory         func([]byte) io.Reader
		hideDestination bool
		wantErr         error
	}{
		{"writer_to_bytes_reader", 128, func(p []byte) io.Reader { return bytes.NewReader(p) }, false, nil},
		{"writer_to_bytes_buffer", 128, func(p []byte) io.Reader { return bytes.NewBuffer(p) }, false, nil},
		{"writer_to_exact", 32, func(p []byte) io.Reader { return bytes.NewReader(p) }, false, nil},
		{"reader_only_empty", 0, func(p []byte) io.Reader { return copyContractReaderOnly{bytes.NewReader(p)} }, false, nil},
		{"reader_only_below", 31, func(p []byte) io.Reader { return copyContractReaderOnly{bytes.NewReader(p)} }, false, nil},
		{"reader_only_exact", 32, func(p []byte) io.Reader { return copyContractReaderOnly{bytes.NewReader(p)} }, false, nil},
		{"reader_only_error_empty", 0, func(p []byte) io.Reader { return &copyContractChunkReader{data: p, terminal: sentinel} }, false, sentinel},
		{"reader_only_error_below", 16, func(p []byte) io.Reader { return &copyContractChunkReader{data: p, terminal: sentinel} }, false, sentinel},
		{"hidden_destination_over", 128, func(p []byte) io.Reader { return copyContractReaderOnly{bytes.NewReader(p)} }, true, nil},
		{"hidden_destination_chunked_error", 64, func(p []byte) io.Reader { return &copyContractChunkReader{data: p, chunk: 7, terminal: sentinel} }, true, sentinel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := payload[:tc.size]
			b := &limitedBuffer{limit: 32}
			var dst io.Writer = b
			if tc.hideDestination {
				dst = copyContractWriterOnly{b}
			}
			n, err := io.Copy(dst, tc.factory(input))
			assertCopyContract(t, b, n, err, input, 32, tc.wantErr)
		})
	}
}

func TestLimitedBufferReaderOnlyContract(t *testing.T) {
	payload := []byte(strings.Repeat("abcdefgh", 16))
	sentinel := errors.New("synthetic reader error")
	for _, tc := range []struct {
		name                string
		size, chunk, prefix int
		terminal            error
		directReadFrom      bool
	}{
		{"one_byte_over", 33, 0, 0, nil, false},
		{"reader_only_over", 64, 0, 0, nil, false},
		{"reader_only_chunked", 128, 7, 0, nil, false},
		{"reader_only_error", 64, 7, 0, sentinel, false},
		{"reader_only_after_prefix", 64, 7, 16, nil, false},
		{"direct_promoted_read_from", 64, 0, 0, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := payload[:tc.size]
			b := &limitedBuffer{limit: 32}
			prefixCount, err := b.Write(input[:tc.prefix])
			if err != nil {
				t.Fatal(err)
			}
			r := copyContractReaderOnly{&copyContractChunkReader{data: input[tc.prefix:], chunk: tc.chunk, terminal: tc.terminal}}
			var n int64
			if tc.directReadFrom {
				n, err = b.ReadFrom(r)
			} else {
				n, err = io.Copy(b, r)
			}
			assertCopyContract(t, b, n+int64(prefixCount), err, input, 32, tc.terminal)
		})
	}
}
