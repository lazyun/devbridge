package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"
)

// Candidate-only controls specify the selected nested-map representation.
func TestSlogGroupCandidateControls(t *testing.T) {
	emit := func(t *testing.T, derive func(*slog.Logger) *slog.Logger, attrs ...slog.Attr) map[string]any {
		t.Helper()
		var output bytes.Buffer
		owner := New(Options{Process: "group-probe", Writer: &output})
		derive(owner.Slog()).LogAttrs(context.Background(), slog.LevelInfo, "probe", attrs...)
		var record map[string]any
		if err := json.Unmarshal(output.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		return record
	}
	identity := func(l *slog.Logger) *slog.Logger { return l }
	check := func(t *testing.T, record map[string]any, want map[string]any) {
		t.Helper()
		if !reflect.DeepEqual(record["data"], want) {
			t.Fatalf("data = %#v, want %#v", record["data"], want)
		}
	}
	t.Run("selected_nested_locations", func(t *testing.T) {
		got := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("outer").WithGroup("inner") }, slog.String("shade", "amber"), slog.String("tree", "birch"))
		check(t, got, map[string]any{"outer": map[string]any{"inner": map[string]any{"shade": "amber", "tree": "birch"}}})
	})
	t.Run("bound_scope_capture", func(t *testing.T) {
		got := emit(t, func(l *slog.Logger) *slog.Logger {
			return l.With("early", "birch").WithGroup("outer").With("middle", "cedar").WithGroup("inner").With("bound", "elm")
		}, slog.String("late", "fir"))
		check(t, got, map[string]any{"early": "birch", "outer": map[string]any{"middle": "cedar", "inner": map[string]any{"bound": "elm", "late": "fir"}}})
	})
	t.Run("derived_branches_independent", func(t *testing.T) {
		got := emit(t, func(l *slog.Logger) *slog.Logger {
			base := l.WithGroup("outer").With("early", "birch")
			_ = base.WithGroup("unused").With("other", "elm")
			return base.WithGroup("inner")
		}, slog.String("late", "fir"))
		check(t, got, map[string]any{"outer": map[string]any{"early": "birch", "inner": map[string]any{"late": "fir"}}})
	})
	t.Run("explicit_named_group_equivalence", func(t *testing.T) {
		grouped := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("branch") }, slog.String("shade", "amber"))
		explicit := emit(t, identity, slog.Group("branch", slog.String("shade", "amber")))
		want := map[string]any{"branch": map[string]any{"shade": "amber"}}
		check(t, grouped, want)
		check(t, explicit, want)
	})
	t.Run("explicit_nested_group_equivalence", func(t *testing.T) {
		grouped := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("outer").WithGroup("inner") }, slog.String("shade", "amber"))
		explicit := emit(t, identity, slog.Group("outer", slog.Group("inner", slog.String("shade", "amber"))))
		want := map[string]any{"outer": map[string]any{"inner": map[string]any{"shade": "amber"}}}
		check(t, grouped, want)
		check(t, explicit, want)
	})
	t.Run("bound_explicit_group_equivalence", func(t *testing.T) {
		grouped := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("branch").With("shade", "amber") })
		explicit := emit(t, func(l *slog.Logger) *slog.Logger { return l.With(slog.Group("branch", slog.String("shade", "amber"))) })
		want := map[string]any{"branch": map[string]any{"shade": "amber"}}
		check(t, grouped, want)
		check(t, explicit, want)
	})
	t.Run("literal_dotted_group_distinct", func(t *testing.T) {
		dotted := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("outer.inner") }, slog.String("shade", "amber"))
		nested := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("outer").WithGroup("inner") }, slog.String("shade", "amber"))
		check(t, dotted, map[string]any{"outer.inner": map[string]any{"shade": "amber"}})
		check(t, nested, map[string]any{"outer": map[string]any{"inner": map[string]any{"shade": "amber"}}})
	})
	t.Run("empty_names_inline", func(t *testing.T) {
		got := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup("").WithGroup("branch").WithGroup("") }, slog.Group("", slog.String("shade", "amber")))
		check(t, got, map[string]any{"branch": map[string]any{"shade": "amber"}})
	})
	t.Run("empty_groups_omitted", func(t *testing.T) {
		got := emit(t, identity, slog.String("shade", "amber"), slog.Group("unused"), slog.Group("outer", slog.Group("inner")))
		check(t, got, map[string]any{"shade": "amber"})
	})
	t.Run("root_routing_preserved", func(t *testing.T) {
		got := emit(t, identity, slog.String("component", "root-part"), slog.String("event", "root.event"), slog.String("shade", "amber"))
		check(t, got, map[string]any{"shade": "amber"})
		if got["component"] != "root-part" || got["event"] != "root.event" {
			t.Fatalf("routing = %#v", got)
		}
	})
	t.Run("grouped_routing_keys_are_data", func(t *testing.T) {
		got := emit(t, func(l *slog.Logger) *slog.Logger {
			return l.With("component", "root-part").With("event", "root.event").WithGroup("branch")
		}, slog.String("component", "inner-part"), slog.String("event", "inner.event"))
		check(t, got, map[string]any{"branch": map[string]any{"component": "inner-part", "event": "inner.event"}})
		if got["component"] != "root-part" || got["event"] != "root.event" {
			t.Fatalf("routing = %#v", got)
		}
	})
	t.Run("reserved_group_names_preserved", func(t *testing.T) {
		for _, name := range []string{"component", "event"} {
			grouped := emit(t, func(l *slog.Logger) *slog.Logger { return l.WithGroup(name) }, slog.String("shade", "amber"))
			explicit := emit(t, identity, slog.Group(name, slog.String("shade", "amber")))
			want := map[string]any{name: map[string]any{"shade": "amber"}}
			check(t, grouped, want)
			check(t, explicit, want)
			for _, got := range []map[string]any{grouped, explicit} {
				if got["component"] != "application" || got["event"] != "log.message" {
					t.Fatalf("default routing = %#v", got)
				}
			}
		}
	})
	t.Run("named_group_depth_boundary", func(t *testing.T) {
		cases := []struct {
			count int
			want  string
		}{
			{7, `{"g":{"g":{"g":{"g":{"g":{"g":{"g":{"shade":"amber"}}}}}}}}`},
			{8, `{"g":{"g":{"g":{"g":{"g":{"g":{"g":{"g":{"shade":"[truncated]"}}}}}}}}}`},
			{9, `{"g":{"g":{"g":{"g":{"g":{"g":{"g":{"g":{"g":"[truncated]"}}}}}}}}}`},
			{10, `{"g":{"g":{"g":{"g":{"g":{"g":{"g":{"g":{"g":"[truncated]"}}}}}}}}}`},
		}
		for _, tc := range cases {
			got := emit(t, func(l *slog.Logger) *slog.Logger {
				for i := 0; i < tc.count; i++ {
					l = l.WithGroup("g")
				}
				return l
			}, slog.String("shade", "amber"))
			var want map[string]any
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			check(t, got, want)
		}
	})
	t.Run("anonymous_group_depth_boundary", func(t *testing.T) {
		for _, count := range []int{8, 9, 10} {
			attr := slog.String("shade", "amber")
			for i := 0; i < count; i++ {
				attr = slog.Group("", attr)
			}
			got := emit(t, identity, attr)
			want := map[string]any{"shade": "amber"}
			if count > 8 {
				want = map[string]any{"[truncated]": true}
			}
			check(t, got, want)
		}
	})
	t.Run("borrowed_nested_map_preserved", func(t *testing.T) {
		borrowed := map[string]any{"inner": map[string]any{"early": "birch"}}
		got := emit(t, identity, slog.Any("outer", borrowed), slog.Group("outer", slog.Group("inner", slog.String("late", "cedar"))))
		check(t, got, map[string]any{"outer": map[string]any{"inner": map[string]any{"early": "birch", "late": "cedar"}}})
		if !reflect.DeepEqual(borrowed, map[string]any{"inner": map[string]any{"early": "birch"}}) {
			t.Fatalf("borrowed input changed: %#v", borrowed)
		}
	})
	t.Run("derived_logger_repeated_emission", func(t *testing.T) {
		var output bytes.Buffer
		owner := New(Options{Process: "group-probe", Writer: &output})
		derived := owner.Slog().WithGroup("outer").With("early", "birch").WithGroup("inner").With("bound", "cedar")
		for _, value := range []string{"elm", "fir"} {
			output.Reset()
			derived.LogAttrs(context.Background(), slog.LevelInfo, "probe", slog.String("late", value))
			var got map[string]any
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			check(t, got, map[string]any{"outer": map[string]any{"early": "birch", "inner": map[string]any{"bound": "cedar", "late": value}}})
		}
	})
}
