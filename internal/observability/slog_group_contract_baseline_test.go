package observability

import (
    "bytes"
    "context"
    "encoding/json"
    "log/slog"
    "reflect"
    "testing"
)

// This finite oracle permits either nested or qualified-flat attribute keys.
// It does not test full slog conformance, sanitization, or concurrent logging.
func TestSlogGroupQualificationContract(t *testing.T) {
    emit := func(t *testing.T, derive func(*slog.Logger) *slog.Logger, attrs ...slog.Attr) map[string]any {
        t.Helper()
        var output bytes.Buffer
        owner := New(Options{Process: "group-probe", Writer: &output})
        logger := derive(owner.Slog())
        logger.LogAttrs(context.Background(), slog.LevelInfo, "probe", attrs...)
        var record map[string]any
        if err := json.Unmarshal(output.Bytes(), &record); err != nil {
            t.Fatalf("decode one synthetic record: %v", err)
        }
        data, ok := record["data"].(map[string]any)
        if !ok {
            t.Fatalf("missing attribute object: %#v", record["data"])
        }
        return data
    }
    identity := func(l *slog.Logger) *slog.Logger { return l }
    var valueCount func(any, string) int
    valueCount = func(v any, want string) int {
        switch x := v.(type) {
        case string:
            if x == want { return 1 }
        case map[string]any:
            n := 0
            for _, child := range x { n += valueCount(child, want) }
            return n
        case []any:
            n := 0
            for _, child := range x { n += valueCount(child, want) }
            return n
        }
        return 0
    }
    preserved := func(t *testing.T, data map[string]any, values ...string) {
        t.Helper()
        for _, value := range values {
            if n := valueCount(data, value); n != 1 {
                t.Fatalf("value %q count = %d, want 1: %#v", value, n, data)
            }
        }
    }
    distinct := func(t *testing.T, a, b map[string]any) {
        t.Helper()
        preserved(t, a, "amber")
        preserved(t, b, "amber")
        if reflect.DeepEqual(a, b) {
            t.Fatalf("different group sequences lost qualification: %#v", a)
        }
    }
    t.Run("ungrouped_control", func(t *testing.T) {
        data := emit(t, identity, slog.String("shade", "amber"))
        if !reflect.DeepEqual(data, map[string]any{"shade": "amber"}) {
            t.Fatalf("ungrouped data = %#v", data)
        }
    })
    t.Run("empty_group_control", func(t *testing.T) {
        plain := emit(t, identity, slog.String("shade", "amber"))
        empty := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("") }, slog.String("shade", "amber"))
        preserved(t, plain, "amber")
        preserved(t, empty, "amber")
        if !reflect.DeepEqual(plain, empty) { t.Fatalf("empty group changed data: %#v / %#v", plain, empty) }
    })
    t.Run("record_attribute_group_names", func(t *testing.T) {
        left := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("left") }, slog.String("shade", "amber"))
        right := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("right") }, slog.String("shade", "amber"))
        distinct(t, left, right)
    })
    t.Run("bound_attribute_group_names", func(t *testing.T) {
        left := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("left").With("shade", "amber") })
        right := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("right").With("shade", "amber") })
        distinct(t, left, right)
    })
    t.Run("accumulated_group_sequence", func(t *testing.T) {
        outer := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("outer") }, slog.String("shade", "amber"))
        nested := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("outer").WithGroup("inner") }, slog.String("shade", "amber"))
        distinct(t, outer, nested)
    })
    t.Run("before_and_after_group_boundary", func(t *testing.T) {
        data := emit(t, func(l *slog.Logger) *slog.Logger { return l.With("early", "birch").WithGroup("branch") }, slog.String("late", "cedar"))
        preserved(t, data, "birch", "cedar")
        if data["early"] != "birch" { t.Fatalf("pre-group attribute moved: %#v", data) }
        if _, exists := data["late"]; exists { t.Fatalf("post-group attribute retained unqualified root key: %#v", data) }
    })
    t.Run("original_logger_control", func(t *testing.T) {
        data := emit(t, func(l *slog.Logger) *slog.Logger {
            _ = l.WithGroup("branch").With("other", "elm")
            return l
        }, slog.String("shade", "amber"))
        if !reflect.DeepEqual(data, map[string]any{"shade": "amber"}) {
            t.Fatalf("derivation changed original logger: %#v", data)
        }
    })
}
