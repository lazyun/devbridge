package git

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func assertStringCopyContract(t *testing.T, b *limitedBuffer, n int64, err error, input string, limit int) {
	t.Helper()
	// Add one only after proving limit is smaller than this tiny payload.
	// This independent oracle never computes MaxInt+1.
	retained := len(input)
	if retained > limit {
		retained = limit + 1
	}
	t.Logf("copied=%d retained=%d limit=%d exceeded=%v err=%v; want copied=%d retained=%d exceeded=%v", n, b.Len(), limit, b.exceeded, err, len(input), retained, len(input) > limit)
	if n != int64(len(input)) || err != nil || b.Len() != retained || b.exceeded != (len(input) > limit) || b.String() != input[:retained] {
		t.Error("string route violated accepted-count, retained-prefix, or exceeded contract")
	}
}

func performStringCopyContract(b *limitedBuffer, route, input string) (int64, error) {
	switch route {
	case "direct_read_from_strings":
		return b.ReadFrom(strings.NewReader(input))
	case "strings_reader":
		return io.Copy(b, strings.NewReader(input))
	case "io_write_string":
		n, err := io.WriteString(b, input)
		return int64(n), err
	case "direct_write_string":
		n, err := b.WriteString(input)
		return int64(n), err
	default:
		panic("unknown test route")
	}
}

// This tiny WriterTo source checks which destination methods ReadFrom exposes.
type stringWriterRouteProbe struct {
	io.Reader
	text                             string
	called, stringWriter, readerFrom bool
}

func (p *stringWriterRouteProbe) WriteTo(w io.Writer) (int64, error) {
	p.called = true
	_, p.stringWriter = w.(io.StringWriter)
	_, p.readerFrom = w.(io.ReaderFrom)
	n, err := io.WriteString(w, p.text)
	return int64(n), err
}

func TestLimitedBufferStringControls(t *testing.T) {
	payload := strings.Repeat("abcdefgh", 16) // At most 128 bytes.
	maxInt := int(^uint(0) >> 1)
	for _, route := range []string{"strings_reader", "io_write_string", "direct_write_string", "direct_read_from_strings"} {
		for _, tc := range []struct {
			name        string
			size, limit int
		}{
			{"empty", 0, 32}, {"below", 31, 32}, {"exact", 32, 32}, {"zero_limit_empty", 0, 0}, {"max_int", 64, maxInt},
		} {
			t.Run(route+"_"+tc.name, func(t *testing.T) {
				b := &limitedBuffer{limit: tc.limit}
				n, err := performStringCopyContract(b, route, payload[:tc.size])
				assertStringCopyContract(t, b, n, err, payload[:tc.size], tc.limit)
			})
		}
	}
}

func TestLimitedBufferStringContract(t *testing.T) {
	payload := strings.Repeat("ab\x00\xffcdef", 16) // 128 bytes, includes NUL and invalid UTF-8.
	for _, route := range []string{"strings_reader", "io_write_string", "direct_write_string", "direct_read_from_strings"} {
		for _, tc := range []struct {
			name               string
			size, limit, chunk int
		}{
			{"one_over", 33, 32, 0}, {"over", 128, 32, 0}, {"zero_limit", 1, 0, 0}, {"chunked", 128, 32, 7},
		} {
			t.Run(route+"_"+tc.name, func(t *testing.T) {
				b := &limitedBuffer{limit: tc.limit}
				input := payload[:tc.size]
				var n int64
				var err error
				if tc.chunk == 0 {
					n, err = performStringCopyContract(b, route, input)
				} else {
					for offset := 0; offset < len(input); {
						end := min(offset+tc.chunk, len(input))
						var count int64
						count, err = performStringCopyContract(b, route, input[offset:end])
						n += count
						if err != nil {
							break
						}
						offset = end
					}
				}
				assertStringCopyContract(t, b, n, err, input, tc.limit)
			})
		}
	}
	t.Run("mixed_byte_and_string_writes", func(t *testing.T) {
		input := payload[:64]
		b := &limitedBuffer{limit: 32}
		count, err := b.Write([]byte(input[:16]))
		if err != nil {
			t.Fatal(err)
		}
		n := int64(count)
		count, err = b.WriteString(input[16:48])
		if err != nil {
			t.Fatal(err)
		}
		n += int64(count)
		more, err := io.Copy(b, strings.NewReader(input[48:]))
		n += more
		assertStringCopyContract(t, b, n, err, input, 32)
	})
	t.Run("read_from_exposes_bounded_string_writer", func(t *testing.T) {
		input := payload[:64]
		b := &limitedBuffer{limit: 32}
		source := &stringWriterRouteProbe{Reader: strings.NewReader(input), text: input}
		n, err := b.ReadFrom(source)
		assertStringCopyContract(t, b, n, err, input, 32)
		if !source.called || !source.stringWriter || source.readerFrom {
			t.Errorf("ReadFrom destination dispatch: called=%v StringWriter=%v ReaderFrom=%v; want true,true,false", source.called, source.stringWriter, source.readerFrom)
		}
	})

	t.Run("explicit_method_set", func(t *testing.T) {
		typ := reflect.TypeOf(&limitedBuffer{})
		var got []string
		for i := 0; i < typ.NumMethod(); i++ {
			got = append(got, typ.Method(i).Name)
		}
		want := []string{"Bytes", "Len", "ReadFrom", "String", "Write", "WriteString"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("public methods=%v, want only %v", got, want)
		}
	})
}
