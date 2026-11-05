package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

type scalarBoolProbe struct{}

func (scalarBoolProbe) LogValue() slog.Value { return slog.BoolValue(true) }

type scalarChainProbe struct{}

func (scalarChainProbe) LogValue() slog.Value { return slog.AnyValue(scalarStringProbe{}) }

func TestSlogScalarCandidateControls(t *testing.T) {
	cases := []struct {
		name      string
		bound     []any
		attrs     []slog.Attr
		data      map[string]any
		component string
		event     string
	}{
		{name: "record_bool", attrs: []slog.Attr{slog.Any("sample", scalarBoolProbe{})}, data: map[string]any{"sample": true}},
		{name: "bound_bool", bound: []any{slog.Any("sample", scalarBoolProbe{})}, data: map[string]any{"sample": true}},
		{name: "record_chain", attrs: []slog.Attr{slog.Any("sample", scalarChainProbe{})}, data: map[string]any{"sample": "resolved-value"}},
		{name: "bound_chain", bound: []any{slog.Any("sample", scalarChainProbe{})}, data: map[string]any{"sample": "resolved-value"}},
		{name: "record_event", attrs: []slog.Attr{slog.Any("event", scalarEventProbe{})}, event: "probe.event"},
		{name: "bound_component", bound: []any{slog.Any("component", scalarComponentProbe{})}, component: "probe-component"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			owner := New(Options{Process: "scalar-probe", Writer: &output})
			logger := owner.Slog()
			if len(tc.bound) != 0 {
				logger = logger.With(tc.bound...)
			}
			logger.LogAttrs(context.Background(), slog.LevelInfo, "scalar probe", tc.attrs...)
			raw := output.Bytes()
			if len(raw) == 0 || raw[len(raw)-1] != '\n' || bytes.Count(raw, []byte{'\n'}) != 1 {
				t.Fatalf("expected exactly one newline-terminated JSON record; got %q", raw)
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var record map[string]any
			if err := decoder.Decode(&record); err != nil {
				t.Fatal(err)
			}
			if record == nil {
				t.Fatal("expected JSON object")
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				t.Fatalf("trailing record or invalid data: %v", err)
			}
			component, event := tc.component, tc.event
			if component == "" {
				component = "application"
			}
			if event == "" {
				event = "log.message"
			}
			for key, want := range map[string]string{"component": component, "event": event, "message": "scalar probe", "process": "scalar-probe", "level": "INFO"} {
				got, ok := record[key].(string)
				if !ok || got != want {
					t.Errorf("%s = %#v, want string %q", key, record[key], want)
				}
			}
			if len(tc.data) == 0 {
				if _, exists := record["data"]; exists {
					t.Errorf("unexpected data: %#v", record["data"])
				}
				return
			}
			data, ok := record["data"].(map[string]any)
			if !ok {
				t.Fatalf("data is not an object: %#v", record["data"])
			}
			if len(data) != len(tc.data) {
				t.Errorf("data length = %d, want %d", len(data), len(tc.data))
			}
			for key, want := range tc.data {
				switch expected := want.(type) {
				case string:
					got, ok := data[key].(string)
					if !ok || got != expected {
						t.Errorf("data.%s = %#v, want string %q", key, data[key], expected)
					}
				case json.Number:
					got, ok := data[key].(json.Number)
					if !ok || got != expected {
						t.Errorf("data.%s = %#v, want number %s", key, data[key], expected)
					}
				case bool:
					got, ok := data[key].(bool)
					if !ok || got != expected {
						t.Errorf("data.%s = %#v, want bool %t", key, data[key], expected)
					}
				default:
					t.Fatalf("unsupported expected type %T", want)
				}
			}
		})
	}
}
