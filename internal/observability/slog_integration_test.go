package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// These finite combinations qualify only the selected diagnostic behavior.
// Every emission uses a fresh buffer-backed owner, not the package default.
type integrationValue struct{ value slog.Value }

func (v integrationValue) LogValue() slog.Value { return v.value }

type integrationCountingValue struct {
	calls *int
	value slog.Value
}

func (v integrationCountingValue) LogValue() slog.Value {
	*v.calls = *v.calls + 1
	return v.value
}

func integrationAttr(key string, value slog.Value) slog.Attr {
	return slog.Any(key, integrationValue{value: value})
}

func integrationRecord(t *testing.T, output *bytes.Buffer) map[string]any {
	t.Helper()
	raw := output.Bytes()
	if len(raw) == 0 || raw[len(raw)-1] != '\n' || bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatalf("expected one newline-terminated record, got %q", raw)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var record map[string]any
	if err := decoder.Decode(&record); err != nil || record == nil {
		t.Fatalf("decode record: object=%#v err=%v", record, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("trailing content: value=%#v err=%v", extra, err)
	}
	return record
}

func integrationData(t *testing.T, record map[string]any, want map[string]any) {
	t.Helper()
	data, present := record["data"]
	if want == nil {
		if present {
			t.Fatalf("data must be omitted, got %#v", data)
		}
		return
	}
	if !present || !reflect.DeepEqual(data, want) {
		t.Fatalf("data = %#v (present=%v), want %#v", data, present, want)
	}
}

func integrationRouting(t *testing.T, record map[string]any, component, event string) {
	t.Helper()
	if record["component"] != component || record["event"] != event {
		t.Fatalf("routing = (%#v, %#v), want (%q, %q)", record["component"], record["event"], component, event)
	}
}

func TestSlogIntegratedGroupsResolutionAndOmission(t *testing.T) {
	t.Run("captured_scopes_resolve_bound_and_record", func(t *testing.T) {
		var output bytes.Buffer
		owner := New(Options{Process: "integration-probe", Writer: &output})
		logger := owner.Slog().With(
			integrationAttr("component", slog.StringValue("root-part")),
			integrationAttr("event", slog.StringValue("root.event")),
			integrationAttr("early", slog.StringValue("birch")),
		).WithGroup("outer").With(
			integrationAttr("quantity", slog.Int64Value(17)), slog.Attr{},
		).WithGroup("inner")
		logger.LogAttrs(context.Background(), slog.LevelInfo, "probe",
			integrationAttr("shade", slog.StringValue("amber")), slog.Attr{},
			slog.String("", ""), slog.Any("present", nil),
			integrationAttr("component", slog.StringValue("inner-part")))
		record := integrationRecord(t, &output)
		integrationRouting(t, record, "root-part", "root.event")
		integrationData(t, record, map[string]any{
			"early": "birch",
			"outer": map[string]any{"quantity": json.Number("17"), "inner": map[string]any{
				"shade": "amber", "": "", "present": nil, "component": "inner-part",
			}},
		})
	})
	t.Run("anonymous_resolved_group_keeps_root_routing", func(t *testing.T) {
		var output bytes.Buffer
		owner := New(Options{Process: "integration-probe", Writer: &output})
		owner.Slog().LogAttrs(context.Background(), slog.LevelInfo, "probe",
			integrationAttr("", slog.GroupValue(
				slog.Attr{}, integrationAttr("component", slog.StringValue("root-part")),
				integrationAttr("event", slog.StringValue("root.event")),
				integrationAttr("shade", slog.StringValue("amber")),
			)))
		record := integrationRecord(t, &output)
		integrationRouting(t, record, "root-part", "root.event")
		integrationData(t, record, map[string]any{"shade": "amber"})
	})
	t.Run("reserved_named_resolved_groups_remain_data", func(t *testing.T) {
		for _, name := range []string{"component", "event"} {
			var output bytes.Buffer
			owner := New(Options{Process: "integration-probe", Writer: &output})
			owner.Slog().LogAttrs(context.Background(), slog.LevelInfo, "probe",
				integrationAttr(name, slog.GroupValue(
					slog.Attr{}, integrationAttr("component", slog.StringValue("inner-part")),
					integrationAttr("event", slog.StringValue("inner.event")),
				)))
			record := integrationRecord(t, &output)
			integrationRouting(t, record, "application", "log.message")
			integrationData(t, record, map[string]any{name: map[string]any{
				"component": "inner-part", "event": "inner.event",
			}})
		}
	})
	t.Run("grouped_zero_only_omits_data", func(t *testing.T) {
		var output bytes.Buffer
		owner := New(Options{Process: "integration-probe", Writer: &output})
		owner.Slog().WithGroup("outer").With(slog.Attr{}).WithGroup("inner").
			LogAttrs(context.Background(), slog.LevelInfo, "probe", slog.Attr{},
				integrationAttr("unused", slog.GroupValue(slog.Attr{})))
		integrationData(t, integrationRecord(t, &output), nil)
	})
	t.Run("resolved_zero_does_not_replace_empty_key", func(t *testing.T) {
		for _, bound := range []bool{false, true} {
			var output bytes.Buffer
			owner := New(Options{Process: "integration-probe", Writer: &output})
			logger := owner.Slog().WithGroup("branch").With(slog.String("", "keep"))
			zero := integrationAttr("", slog.Value{})
			if bound {
				logger = logger.With(zero)
				logger.LogAttrs(context.Background(), slog.LevelInfo, "probe")
			} else {
				logger.LogAttrs(context.Background(), slog.LevelInfo, "probe", zero)
			}
			integrationData(t, integrationRecord(t, &output), map[string]any{"branch": map[string]any{"": "keep"}})
		}
	})
	t.Run("resolved_nonzero_empty_and_nil_controls", func(t *testing.T) {
		var typedNil *int
		cases := []struct {
			key   string
			value slog.Value
			want  any
		}{
			{"", slog.StringValue(""), ""},
			{"", slog.BoolValue(false), false},
			{"", slog.IntValue(0), json.Number("0")},
			{"", slog.AnyValue(typedNil), nil},
			{"present", slog.Value{}, nil},
		}
		for _, tc := range cases {
			for _, bound := range []bool{false, true} {
				var output bytes.Buffer
				owner := New(Options{Process: "integration-probe", Writer: &output})
				logger := owner.Slog().WithGroup("branch")
				attr := integrationAttr(tc.key, tc.value)
				if bound {
					logger = logger.With(attr, slog.Attr{})
					logger.LogAttrs(context.Background(), slog.LevelInfo, "probe")
				} else {
					logger.LogAttrs(context.Background(), slog.LevelInfo, "probe", attr, slog.Attr{})
				}
				integrationData(t, integrationRecord(t, &output), map[string]any{"branch": map[string]any{tc.key: tc.want}})
			}
		}
	})
	t.Run("resolved_groups_copy_borrowed_nested_maps", func(t *testing.T) {
		var output bytes.Buffer
		owner := New(Options{Process: "integration-probe", Writer: &output})
		borrowed := map[string]any{"inner": map[string]any{"early": "birch"}}
		owner.Slog().With(integrationAttr("outer", slog.AnyValue(borrowed))).
			LogAttrs(context.Background(), slog.LevelInfo, "probe",
				integrationAttr("outer", slog.GroupValue(
					integrationAttr("inner", slog.GroupValue(slog.Attr{}, integrationAttr("late", slog.StringValue("cedar")))),
				)))
		integrationData(t, integrationRecord(t, &output), map[string]any{"outer": map[string]any{
			"inner": map[string]any{"early": "birch", "late": "cedar"},
		}})
		if !reflect.DeepEqual(borrowed, map[string]any{"inner": map[string]any{"early": "birch"}}) {
			t.Fatalf("borrowed input changed: %#v", borrowed)
		}
	})
	t.Run("resolved_group_repeated_emission_isolated", func(t *testing.T) {
		var output bytes.Buffer
		owner := New(Options{Process: "integration-probe", Writer: &output})
		logger := owner.Slog().WithGroup("outer").With(integrationAttr("inner", slog.GroupValue(
			slog.Attr{}, integrationAttr("early", slog.StringValue("birch")),
		)))
		logger.LogAttrs(context.Background(), slog.LevelInfo, "probe", slog.Group("inner", integrationAttr("late", slog.StringValue("cedar"))))
		integrationData(t, integrationRecord(t, &output), map[string]any{"outer": map[string]any{
			"inner": map[string]any{"early": "birch", "late": "cedar"},
		}})
		output.Reset()
		logger.LogAttrs(context.Background(), slog.LevelInfo, "probe", slog.Attr{})
		integrationData(t, integrationRecord(t, &output), map[string]any{"outer": map[string]any{
			"inner": map[string]any{"early": "birch"},
		}})
	})
	t.Run("bound_nested_values_resolve_per_emission", func(t *testing.T) {
		var output bytes.Buffer
		owner := New(Options{Process: "integration-probe", Writer: &output})
		calls := 0
		logger := owner.Slog().WithGroup("outer").With(integrationAttr("inner", slog.GroupValue(
			slog.Attr{}, slog.Any("shade", integrationCountingValue{calls: &calls, value: slog.StringValue("amber")}),
		)))
		if calls != 0 {
			t.Fatalf("binding resolved the value %d times", calls)
		}
		for emission := 1; emission <= 2; emission++ {
			output.Reset()
			logger.LogAttrs(context.Background(), slog.LevelInfo, "probe", slog.Attr{})
			if calls != emission {
				t.Fatalf("after emission %d: resolution count = %d", emission, calls)
			}
			integrationData(t, integrationRecord(t, &output), map[string]any{"outer": map[string]any{
				"inner": map[string]any{"shade": "amber"},
			}})
		}
	})
	t.Run("resolved_empty_groups_omitted", func(t *testing.T) {
		for _, key := range []string{"", "unused"} {
			for _, bound := range []bool{false, true} {
				var output bytes.Buffer
				owner := New(Options{Process: "integration-probe", Writer: &output})
				logger := owner.Slog().WithGroup("branch")
				attr := integrationAttr(key, slog.GroupValue())
				if bound {
					logger = logger.With(attr, slog.Attr{})
					logger.LogAttrs(context.Background(), slog.LevelInfo, "probe")
				} else {
					logger.LogAttrs(context.Background(), slog.LevelInfo, "probe", attr, slog.Attr{})
				}
				integrationData(t, integrationRecord(t, &output), nil)
			}
		}
	})
	t.Run("resolved_groups_share_depth_budget", func(t *testing.T) {
		for _, count := range []int{7, 8, 9, 10} {
			var output bytes.Buffer
			owner := New(Options{Process: "integration-probe", Writer: &output})
			attr := integrationAttr("shade", slog.StringValue("amber"))
			for i := 0; i < count; i++ {
				attr = integrationAttr("", slog.GroupValue(slog.Attr{}, attr))
			}
			owner.Slog().LogAttrs(context.Background(), slog.LevelInfo, "probe", attr)
			want := map[string]any{"shade": "amber"}
			if count > 8 {
				want = map[string]any{"[truncated]": true}
			}
			integrationData(t, integrationRecord(t, &output), want)
		}
	})
}

func TestSlogIntegratedBasicDiagnosticRedaction(t *testing.T) {
	// Synthetic marker, not a real credential or a claim about arbitrary input.
	const material = "SYNTHETIC_BASIC_MATERIAL"
	const plain = "basic birch control"
	const diagnostic = "note Authorization: Basic " + material + ", tail"
	const redacted = "note Authorization=[redacted] [redacted], tail"
	t.Run("message_and_grouped_resolved_values", func(t *testing.T) {
		var output bytes.Buffer
		owner := New(Options{Process: "integration-probe", Writer: &output})
		owner.Slog().With(integrationAttr("rootNote", slog.StringValue(diagnostic))).
			WithGroup("branch").With(integrationAttr("boundNote", slog.StringValue(diagnostic)), slog.Attr{}).
			LogAttrs(context.Background(), slog.LevelInfo, diagnostic,
				integrationAttr("nested", slog.GroupValue(slog.Attr{},
					integrationAttr("recordNote", slog.StringValue(diagnostic)), slog.String("plain", plain))))
		if strings.Contains(output.String(), material) {
			t.Fatal("synthetic Basic material survived diagnostic redaction")
		}
		record := integrationRecord(t, &output)
		if record["message"] != redacted {
			t.Fatalf("message = %#v, want %q", record["message"], redacted)
		}
		integrationData(t, record, map[string]any{
			"rootNote": redacted,
			"branch": map[string]any{"boundNote": redacted, "nested": map[string]any{
				"recordNote": redacted, "plain": plain,
			}},
		})
	})
	t.Run("proxy_assignment_and_plain_controls", func(t *testing.T) {
		var output bytes.Buffer
		owner := New(Options{Process: "integration-probe", Writer: &output})
		owner.Slog().WithGroup("branch").LogAttrs(context.Background(), slog.LevelInfo, "probe",
			slog.Attr{}, integrationAttr("note", slog.StringValue("Proxy-Authorization=Basic "+material+"; tail")),
			integrationAttr("plain", slog.StringValue(plain)))
		if strings.Contains(output.String(), material) {
			t.Fatal("synthetic proxy Basic material survived diagnostic redaction")
		}
		integrationData(t, integrationRecord(t, &output), map[string]any{"branch": map[string]any{
			"note": "Proxy-Authorization=[redacted] [redacted]; tail", "plain": plain,
		}})
	})
}

// Raw zero attrs are known before qualification; they must not create a
// truncation marker merely because the captured group path is deep.
func TestSlogIntegratedDeepZeroAttrControls(t *testing.T) {
	grouped := func(logger *slog.Logger, count int) *slog.Logger {
		for i := 0; i < count; i++ {
			logger = logger.WithGroup("g")
		}
		return logger
	}
	truncated := map[string]any{"g": "[truncated]"}
	for i := 0; i < 8; i++ {
		truncated = map[string]any{"g": truncated}
	}
	t.Run("raw_record_zero_only", func(t *testing.T) {
		for _, count := range []int{9, 10} {
			var output bytes.Buffer
			owner := New(Options{Process: "integration-probe", Writer: &output})
			grouped(owner.Slog(), count).LogAttrs(context.Background(), slog.LevelInfo, "probe", slog.Attr{}, slog.Any("", nil))
			record := integrationRecord(t, &output)
			if data, present := record["data"]; present {
				t.Errorf("%d groups: raw record zeros emitted data %#v", count, data)
			}
		}
	})
	t.Run("raw_bound_zero_only", func(t *testing.T) {
		for _, count := range []int{9, 10} {
			var output bytes.Buffer
			owner := New(Options{Process: "integration-probe", Writer: &output})
			grouped(owner.Slog(), count).With(slog.Attr{}, slog.Any("", nil)).
				LogAttrs(context.Background(), slog.LevelInfo, "probe", slog.Attr{})
			record := integrationRecord(t, &output)
			if data, present := record["data"]; present {
				t.Errorf("%d groups: raw bound zeros emitted data %#v", count, data)
			}
		}
	})
	t.Run("raw_zeros_keep_root_empty_key", func(t *testing.T) {
		for _, count := range []int{9, 10} {
			var output bytes.Buffer
			owner := New(Options{Process: "integration-probe", Writer: &output})
			grouped(owner.Slog().With(slog.String("", "keep")), count).
				With(slog.Attr{}, slog.Any("", nil)).LogAttrs(context.Background(), slog.LevelInfo, "probe", slog.Attr{})
			record := integrationRecord(t, &output)
			if !reflect.DeepEqual(record["data"], map[string]any{"": "keep"}) {
				t.Errorf("%d groups: raw zeros changed root data %#v", count, record["data"])
			}
		}
	})
	t.Run("nonzero_values_keep_truncation", func(t *testing.T) {
		var typedNil *int
		for _, count := range []int{9, 10} {
			for _, attr := range []slog.Attr{slog.String("", ""), slog.Bool("", false), slog.Int("", 0), slog.Any("", typedNil), slog.Any("present", nil)} {
				var output bytes.Buffer
				owner := New(Options{Process: "integration-probe", Writer: &output})
				grouped(owner.Slog(), count).LogAttrs(context.Background(), slog.LevelInfo, "probe", attr, slog.Attr{})
				integrationData(t, integrationRecord(t, &output), truncated)
			}
		}
	})
	t.Run("deep_values_remain_unresolved", func(t *testing.T) {
		for _, count := range []int{9, 10} {
			var output bytes.Buffer
			owner := New(Options{Process: "integration-probe", Writer: &output})
			boundCalls, recordCalls := 0, 0
			grouped(owner.Slog(), count).With(slog.Attr{},
				slog.Any("bound", integrationCountingValue{calls: &boundCalls, value: slog.StringValue("birch")})).
				LogAttrs(context.Background(), slog.LevelInfo, "probe", slog.Attr{},
					slog.Any("record", integrationCountingValue{calls: &recordCalls, value: slog.StringValue("cedar")}))
			if boundCalls != 0 || recordCalls != 0 {
				t.Errorf("%d groups: values below depth limit resolved: bound=%d record=%d", count, boundCalls, recordCalls)
			}
			integrationData(t, integrationRecord(t, &output), truncated)
		}
	})
}
