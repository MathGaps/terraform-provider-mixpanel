package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// liveBoard builds a dashboards GET layout: rows in order, each a list of
// card ids at the given width.
func liveBoard(rows ...[2]any) map[string]any {
	m := map[string]any{}
	order := []any{}
	for _, r := range rows {
		id := r[0].(string)
		cells := []any{}
		for _, c := range r[1].([]string) {
			cells = append(cells, map[string]any{"id": c, "width": 12.0, "content_id": 1.0, "content_type": "report"})
		}
		m[id] = map[string]any{"height": 0.0, "cells": cells}
		order = append(order, id)
	}
	return map[string]any{"rows": m, "order": order, "version": "2.0.0"}
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// acceptedByServer checks a PATCH layout against the rules the dashboards
// API enforces (live-verified 2026-11-04; see dashboard_layout.go): every live
// row listed in rows and rows_order, every card placed exactly once, no row id
// the board does not hold.
func acceptedByServer(t *testing.T, patch map[string]any, live map[string]any) {
	t.Helper()
	liveRows := live["rows"].(map[string]any)
	cards := map[string]int{}
	for _, r := range liveRows {
		for _, c := range r.(map[string]any)["cells"].([]any) {
			cards[c.(map[string]any)["id"].(string)] = 0
		}
	}
	listed := map[string]bool{}
	for _, ra := range patch["rows"].([]any) {
		r := ra.(map[string]any)
		if id, ok := r["id"].(string); ok {
			if _, onBoard := liveRows[id]; !onBoard {
				t.Fatalf("names row %q, which the board does not hold", id)
			}
			listed[id] = true
		} else if _, ok := r["temp_id"].(string); !ok {
			t.Fatalf("row has neither id nor temp_id: %v", r)
		}
		for _, ca := range r["cells"].([]any) {
			cards[ca.(map[string]any)["id"].(string)]++
		}
	}
	for id := range liveRows {
		if !listed[id] {
			t.Fatalf("leaves out live row %q", id)
		}
	}
	for c, n := range cards {
		if n != 1 {
			t.Fatalf("places card %q %d times", c, n)
		}
	}
	if len(patch["rows_order"].([]any)) != len(patch["rows"].([]any)) {
		t.Fatalf("rows_order does not list every row")
	}
}

func TestReconcileDashboardLayout_groupsCardsUnderStaleRowIDs(t *testing.T) {
	t.Parallel()
	live := liveBoard(
		[2]any{"rA", []string{"c1"}},
		[2]any{"rB", []string{"c2"}},
		[2]any{"rC", []string{"c3"}},
		[2]any{"rD", []string{"c4"}},
	)
	// c1 and c2 share a row; one row names an id the board no longer holds,
	// one names none; rB and rD are not named at all.
	desired := decode(t, `{"rows":[
		{"id":"gone","height":0,"cells":[{"id":"c2","width":6},{"id":"c1","width":6}]},
		{"cells":[{"id":"c4","width":4},{"id":"c3","width":8}]}]}`)
	got, err := reconcileDashboardLayout(desired, live)
	if err != nil {
		t.Fatal(err)
	}
	acceptedByServer(t, got, live)
	if arr, want := layoutArrangement(mustRows(t, got), nil),
		`[{"height":0,"cells":[{"id":"c2","width":6},{"id":"c1","width":6}]},{"height":0,"cells":[{"id":"c4","width":4},{"id":"c3","width":8}]}]`; arr != want {
		t.Fatalf("arrangement:\n got %s\nwant %s", arr, want)
	}
}

func TestReconcileDashboardLayout_keepsUnplacedCards(t *testing.T) {
	t.Parallel()
	live := liveBoard([2]any{"rA", []string{"c1", "c2"}}, [2]any{"rB", []string{"c3"}})
	got, err := reconcileDashboardLayout(decode(t, `{"rows":[{"cells":[{"id":"c3","width":12}]}]}`), live)
	if err != nil {
		t.Fatal(err)
	}
	acceptedByServer(t, got, live)
	if arr := layoutArrangement(mustRows(t, got), nil); !strings.HasPrefix(arr, `[{"height":0,"cells":[{"id":"c3"`) || !strings.Contains(arr, `"c1"`) {
		t.Fatalf("configured row first, unplaced cards after it: %s", arr)
	}
}

func TestReconcileDashboardLayout_refusesCardsNotOnTheBoard(t *testing.T) {
	t.Parallel()
	live := liveBoard([2]any{"rA", []string{"c1"}})
	if _, err := reconcileDashboardLayout(decode(t, `{"rows":[{"cells":[{"id":"zz","width":12}]}]}`), live); err == nil || !strings.Contains(err.Error(), `"zz"`) {
		t.Fatalf("want an error naming the unknown card, got %v", err)
	}
	if _, err := reconcileDashboardLayout(decode(t, `{"rows":[{"cells":[{"id":"c1","width":6},{"id":"c1","width":6}]}]}`), live); err == nil {
		t.Fatal("want an error for a card placed twice")
	}
}

func TestLayoutMatchesLive_ignoresRowIDs(t *testing.T) {
	t.Parallel()
	live := liveBoard([2]any{"server1", []string{"c1", "c2"}}, [2]any{"server2", []string{"c3"}})
	same := `{"rows":[{"id":"mine","height":0,"cells":[{"id":"c1","width":12},{"id":"c2","width":12}]}]}`
	if !layoutMatchesLive(same, live) {
		t.Fatal("same arrangement under other row ids (c3 unplaced) should match")
	}
	moved := `{"rows":[{"height":0,"cells":[{"id":"c1","width":12}]},{"height":0,"cells":[{"id":"c2","width":12}]}]}`
	if layoutMatchesLive(moved, live) {
		t.Fatal("a different arrangement must not match")
	}
}

func mustRows(t *testing.T, patch map[string]any) []layoutRow {
	t.Helper()
	rows, err := parseWriteRows(patch["rows"])
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
