package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"testing"
)

func TestEmptyAttrContract(t *testing.T) {
	cases := []struct {
		name   string
		bound  [][]slog.Attr
		record []slog.Attr
		want   map[string]any
	}{
		{name: "record_zero", record: []slog.Attr{{}}},
		{name: "bound_zero", bound: [][]slog.Attr{{{}}}},
		{name: "record_any_nil", record: []slog.Attr{slog.Any("", nil)}},
		{name: "bound_any_nil", bound: [][]slog.Attr{{slog.Any("", nil)}}},
		{name: "record_keep_then_zero", record: []slog.Attr{slog.String("", "keep"), {}}, want: map[string]any{"": "keep"}},
		{name: "bound_keep_record_zero", bound: [][]slog.Attr{{slog.String("", "keep")}}, record: []slog.Attr{{}}, want: map[string]any{"": "keep"}},
		{name: "consecutive_with_keep_zero", bound: [][]slog.Attr{{slog.String("", "keep")}, {{}}}, want: map[string]any{"": "keep"}},
		{name: "record_empty_string", record: []slog.Attr{slog.String("", "")}, want: map[string]any{"": ""}},
		{name: "bound_empty_string", bound: [][]slog.Attr{{slog.String("", "")}}, want: map[string]any{"": ""}},
		{name: "record_named_nil", record: []slog.Attr{slog.Any("present", nil)}, want: map[string]any{"present": nil}},
		{name: "bound_named_nil", bound: [][]slog.Attr{{slog.Any("present", nil)}}, want: map[string]any{"present": nil}},
		{name: "zero_before_keep", record: []slog.Attr{{}, slog.String("", "keep")}, want: map[string]any{"": "keep"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			owner := New(Options{Process: "empty-attr-probe", Writer: &buffer})
			logger := owner.Slog()
			for _, attrs := range tc.bound {
				args := make([]any, len(attrs))
				for i, attr := range attrs {
					args[i] = attr
				}
				logger = logger.With(args...)
			}
			logger.LogAttrs(context.Background(), slog.LevelInfo, "empty attr probe", tc.record...)
			raw := buffer.Bytes()
			if len(raw) == 0 || raw[len(raw)-1] != '\n' || bytes.Count(raw, []byte{'\n'}) != 1 {
				t.Fatalf("expected exactly one newline-terminated record, got %q", raw)
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			var got map[string]any
			if err := decoder.Decode(&got); err != nil || got == nil {
				t.Fatalf("expected JSON object: got=%#v err=%v", got, err)
			}
			var trailing any
			if err := decoder.Decode(&trailing); err != io.EOF {
				t.Fatalf("unexpected trailing content: value=%#v err=%v", trailing, err)
			}
			for key, want := range map[string]any{
				"message": "empty attr probe", "process": "empty-attr-probe", "level": "INFO",
				"component": "application", "event": "log.message",
			} {
				if got[key] != want {
					t.Errorf("%s = %#v, want %#v", key, got[key], want)
				}
			}
			data, present := got["data"]
			if tc.want == nil {
				if present {
					t.Errorf("data must be omitted, got %#v", data)
				}
				return
			}
			object, ok := data.(map[string]any)
			if !present || !ok || len(object) != len(tc.want) || !reflect.DeepEqual(object, tc.want) {
				t.Errorf("data = %#v (present=%v), want exactly %#v", data, present, tc.want)
			}
		})
	}
}
