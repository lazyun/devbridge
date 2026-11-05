package git

import (
	"bytes"
	"io"
	"testing"
)

func TestLimitedBufferMaxIntControls(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name           string
		supplied, want int
	}{
		{"minimum_int", -maxInt - 1, DefaultMaxOutput},
		{"negative", -1, DefaultMaxOutput},
		{"zero", 0, DefaultMaxOutput},
		{"one", 1, 1},
		{"maximum_minus_one", maxInt - 1, maxInt - 1},
		{"maximum", maxInt, maxInt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(Config{MaxOutput: tc.supplied})
			t.Logf("MaxOutput input=%d normalized=%d want=%d", tc.supplied, c.maxOutput, tc.want)
			if c.maxOutput != tc.want {
				t.Errorf("normalized MaxOutput=%d, want %d", c.maxOutput, tc.want)
			}
		})
	}
	payload := bytes.Repeat([]byte("abcd"), 16) // 64 bytes; never allocate from limit.
	t.Run("maximum_minus_one_write", func(t *testing.T) {
		b := &limitedBuffer{limit: maxInt - 1}
		n, err := b.Write(payload)
		if err != nil || n != len(payload) || !bytes.Equal(b.Bytes(), payload) || b.exceeded {
			t.Errorf("count=%d retained=%d exceeded=%v err=%v", n, b.Len(), b.exceeded, err)
		}
	})
	t.Run("maximum_empty_write", func(t *testing.T) {
		b := &limitedBuffer{limit: maxInt}
		n, err := b.Write(nil)
		if err != nil || n != 0 || b.Len() != 0 || b.exceeded {
			t.Errorf("count=%d retained=%d exceeded=%v err=%v", n, b.Len(), b.exceeded, err)
		}
	})
	t.Run("maximum_reader_only_copy", func(t *testing.T) {
		b := &limitedBuffer{limit: maxInt}
		n, err := io.Copy(b, copyContractReaderOnly{bytes.NewReader(payload)})
		if err != nil || n != int64(len(payload)) || !bytes.Equal(b.Bytes(), payload) || b.exceeded {
			t.Errorf("count=%d retained=%d exceeded=%v err=%v", n, b.Len(), b.exceeded, err)
		}
	})
}

func TestLimitedBufferMaxIntContract(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	payload := bytes.Repeat([]byte("abcd"), 16) // 64 bytes, independently expected in full.
	for _, route := range []string{"direct_write", "chunked_write", "writer_to_copy"} {
		t.Run(route, func(t *testing.T) {
			client := NewClient(Config{MaxOutput: maxInt})
			b := &limitedBuffer{limit: client.maxOutput}
			var n int64
			var err error
			switch route {
			case "direct_write":
				var count int
				count, err = b.Write(payload)
				n = int64(count)
			case "chunked_write":
				for offset := 0; offset < len(payload); {
					end := min(offset+7, len(payload))
					var count int
					count, err = b.Write(payload[offset:end])
					n += int64(count)
					if err != nil {
						break
					}
					offset = end
				}
			case "writer_to_copy":
				n, err = io.Copy(b, bytes.NewReader(payload))
			}
			t.Logf("limit=%d copied=%d retained=%d exceeded=%v err=%v; want copied=64 retained=64 exceeded=false err=nil", b.limit, n, b.Len(), b.exceeded, err)
			if n != 64 || err != nil || b.Len() != 64 || b.exceeded || !bytes.Equal(b.Bytes(), payload) {
				t.Errorf("tiny payload under MaxInt limit was not preserved")
			}
		})
	}
}
