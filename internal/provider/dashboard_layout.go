package provider

// Board layout reconciliation (live-verified 2026-11-04 against a scratch
// board in a real project). The dashboards PATCH accepts a layout only when it
// accounts for the board as it stands:
//
//   - every row the board holds is listed in `rows` and `rows_order` (a row to
//     remove is listed with `cells: []`; the server then drops it);
//   - every card on the board is placed exactly once;
//   - a row id the board does not hold is refused (new rows use `temp_id`);
//   - cards may move freely between rows and into new rows.
//
// Anything else is refused with 403 INVALID_LAYOUT and no reason. Row ids
// change whenever someone edits the board in the webapp, so a configured
// layout cannot be expected to name them. The provider therefore treats a row
// id in config as a hint: Update reads the live board and rebuilds the PATCH
// so it satisfies the rules above, and Read keeps the configured value when
// the live board has the same arrangement under different row ids.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type layoutCell struct {
	id    string
	width any
	raw   map[string]any
}

type layoutRow struct {
	id     string // real row id ("" when the row carries temp_id or none)
	tempID string
	height any
	cells  []layoutCell
}

// liveLayout is the board's layout from the dashboards GET shape.
type liveLayout struct {
	rows     []layoutRow
	cellRow  map[string]string // cell id -> live row id
	rowIndex map[string]int
}

func parseLiveLayout(v any) (liveLayout, error) {
	wf, ok := dashboardLayoutWriteFormat(v)
	if !ok {
		return liveLayout{}, fmt.Errorf("unexpected live layout shape")
	}
	rows, err := parseWriteRows(wf["rows"])
	if err != nil {
		return liveLayout{}, err
	}
	l := liveLayout{rows: rows, cellRow: map[string]string{}, rowIndex: map[string]int{}}
	for i, r := range rows {
		l.rowIndex[r.id] = i
		for _, c := range r.cells {
			l.cellRow[c.id] = r.id
		}
	}
	return l, nil
}

func parseWriteRows(v any) ([]layoutRow, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("layout.rows must be a list")
	}
	out := make([]layoutRow, 0, len(list))
	for i, ra := range list {
		rm, ok := ra.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("layout.rows[%d] must be an object", i)
		}
		row := layoutRow{height: rm["height"]}
		row.id, _ = rm["id"].(string)
		row.tempID, _ = rm["temp_id"].(string)
		cells, _ := rm["cells"].([]any)
		for j, ca := range cells {
			cm, ok := ca.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("layout.rows[%d].cells[%d] must be an object", i, j)
			}
			cid, _ := cm["id"].(string)
			row.cells = append(row.cells, layoutCell{id: cid, width: cm["width"], raw: cm})
		}
		out = append(out, row)
	}
	return out, nil
}

// reconcileDashboardLayout turns a configured layout (write format; row ids
// optional) into a PATCH body the server accepts for the board as it stands.
// Cards the config does not place keep their live rows, after the configured
// ones; live rows left with no cards are listed empty so the server drops
// them.
func reconcileDashboardLayout(desired map[string]any, liveGet any) (map[string]any, error) {
	live, err := parseLiveLayout(liveGet)
	if err != nil {
		return nil, err
	}
	rows, err := parseWriteRows(desired["rows"])
	if err != nil {
		return nil, err
	}
	placed := map[string]bool{}
	for i, r := range rows {
		for _, c := range r.cells {
			if c.id == "" {
				continue // a temp_id cell: passed through as written
			}
			if _, onBoard := live.cellRow[c.id]; !onBoard {
				return nil, fmt.Errorf("layout.rows[%d] places card %q, which is not on the board (its cards are %s)", i, c.id, strings.Join(sortedKeys(live.cellRow), ", "))
			}
			if placed[c.id] {
				return nil, fmt.Errorf("layout places card %q more than once", c.id)
			}
			placed[c.id] = true
		}
	}

	assigned, used := assignRowIDs(rows, live)
	out := make([]any, 0, len(live.rows)+len(rows))
	order := make([]any, 0, cap(out))
	temp := 0
	nextTemp := func() string { temp++; return fmt.Sprintf("tf-row-%d", temp) }
	for i, r := range rows {
		id := assigned[i]
		height := r.height
		if height == nil {
			height = liveHeight(live, id)
		}
		row := map[string]any{"height": height, "cells": cellsOut(r.cells)}
		switch {
		case id != "":
			row["id"] = id
		case r.tempID != "":
			row["temp_id"] = r.tempID
		default:
			row["temp_id"] = nextTemp()
		}
		out = append(out, row)
		order = append(order, firstRowRef(row["id"], row["temp_id"]))
	}

	// Cards the config does not place stay on the board, in their live rows.
	for _, lr := range live.rows {
		var rest []layoutCell
		for _, c := range lr.cells {
			if !placed[c.id] {
				rest = append(rest, c)
			}
		}
		row := map[string]any{"height": lr.height, "cells": cellsOut(rest)}
		switch {
		case !used[lr.id]:
			row["id"] = lr.id
			used[lr.id] = true
		case len(rest) > 0:
			row["temp_id"] = nextTemp()
		default:
			continue
		}
		out = append(out, row)
		order = append(order, firstRowRef(row["id"], row["temp_id"]))
	}
	return map[string]any{"rows": out, "rows_order": order}, nil
}

// assignRowIDs gives each configured row a live row id where it can: the id
// it names, when the board still holds that row and no earlier row took it;
// otherwise the live row holding its first card, when free. A row left
// without one is created (temp_id).
func assignRowIDs(rows []layoutRow, live liveLayout) ([]string, map[string]bool) {
	assigned := make([]string, len(rows))
	used := map[string]bool{}
	for i, r := range rows {
		if _, ok := live.rowIndex[r.id]; ok && !used[r.id] {
			assigned[i] = r.id
			used[r.id] = true
		}
	}
	for i, r := range rows {
		if assigned[i] != "" || r.tempID != "" || len(r.cells) == 0 {
			continue
		}
		if lr := live.cellRow[r.cells[0].id]; lr != "" && !used[lr] {
			assigned[i] = lr
			used[lr] = true
		}
	}
	return assigned, used
}

func cellsOut(cells []layoutCell) []any {
	out := make([]any, 0, len(cells))
	for _, c := range cells {
		m := map[string]any{"width": c.width}
		if c.id != "" {
			m["id"] = c.id
		} else {
			for k, v := range c.raw {
				m[k] = v
			}
		}
		out = append(out, m)
	}
	return out
}

func liveHeight(l liveLayout, id string) any {
	if i, ok := l.rowIndex[id]; ok && l.rows[i].height != nil {
		return l.rows[i].height
	}
	return 0
}

func firstRowRef(vs ...any) any {
	for _, v := range vs {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// layoutArrangement is a layout with its row ids dropped: the rows in order,
// each its height and its cards with their widths. Cards outside `only` (when
// given) are left out, and rows left with no cards are dropped.
func layoutArrangement(rows []layoutRow, only map[string]bool) string {
	type cell struct {
		ID    string `json:"id"`
		Width any    `json:"width"`
	}
	type row struct {
		Height any    `json:"height"`
		Cells  []cell `json:"cells"`
	}
	out := []row{}
	for _, r := range rows {
		var cs []cell
		for _, c := range r.cells {
			if only == nil || only[c.id] {
				cs = append(cs, cell{c.id, jsonNumber(c.width)})
			}
		}
		if len(cs) == 0 {
			continue
		}
		h := jsonNumber(r.height)
		if h == nil {
			h = 0.0
		}
		out = append(out, row{h, cs})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func jsonNumber(v any) any {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return v
}

// layoutMatchesLive reports whether the live board shows the configured
// arrangement: the same cards in the same rows, order and widths, whatever
// the row ids. Cards the configuration does not place are ignored, as
// reconcileDashboardLayout leaves them where they are.
func layoutMatchesLive(configured string, liveGet any) bool {
	var desired map[string]any
	if err := json.Unmarshal([]byte(configured), &desired); err != nil {
		return false
	}
	rows, err := parseWriteRows(desired["rows"])
	if err != nil {
		return false
	}
	live, err := parseLiveLayout(liveGet)
	if err != nil {
		return false
	}
	named := map[string]bool{}
	for _, r := range rows {
		for _, c := range r.cells {
			if c.id == "" {
				return false // a temp_id cell has no stable id to compare
			}
			named[c.id] = true
		}
	}
	return layoutArrangement(rows, nil) == layoutArrangement(live.rows, named)
}
