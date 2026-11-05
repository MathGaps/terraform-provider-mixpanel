// Mock-server test harness shared by the generated per-entity lifecycle tests.
//
// These are acceptance tests (resource.Test, gated by TF_ACC) but they run
// against an in-process httptest echo server instead of the real Mixpanel API:
// the provider's base_url is pointed at the mock, so a full plan -> apply ->
// refresh -> destroy cycle exercises the real Terraform graph and the generic
// tftypes<->JSON bridge with no credentials and nothing created externally.
//
// The mock is deliberately generic: it round-trips whatever the provider sends
// (POST stores the body and assigns an id; GET returns it; PATCH/PUT merges;
// DELETE removes), enveloping the response per the entity's contract. That makes
// the post-apply empty-plan check a real idempotency test of the bridge for
// every entity, without per-entity recorded response fixtures.
package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// importIDFunc returns an ImportStateIdFunc that reconstructs the import id from the
// prior state of the named resource. idAttr is the resource's identity attribute
// (ent.identity_attr). scopeAttr names the scope attribute whose value prefixes the
// id as "<scope>:<id>" — "project_id" for project/workspace-scoped resources,
// "organization_id" for org-scoped resources, and "" for unscoped/singleton
// resources (the bare id, which for a singleton already equals the project id).
// This matches the composite, scope-aware ImportState parser. The default provider
// scope value ("1") backstops an empty prior-state scope attribute.
func importIDFunc(resourceName, idAttr, scopeAttr string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("resource %s not found in state", resourceName)
		}
		id := rs.Primary.Attributes[idAttr]
		if id == "" {
			id = rs.Primary.ID
		}
		if scopeAttr == "" {
			return id, nil
		}
		scope := rs.Primary.Attributes[scopeAttr]
		if scope == "" {
			// Provider default from providerConfig.
			scope = "1"
		}
		return scope + ":" + id, nil
	}
}

// testProtoV6 wires the provider under test for resource.Test.
var testProtoV6 = map[string]func() (tfprotov6.ProviderServer, error){
	"mixpanel": providerserver.NewProtocol6WithError(New("test")()),
}

// mockOpts describes the response contract of one entity so the generic echo
// server can answer it faithfully. Everything else (paths, request shape) the
// server infers from the request itself.
type mockOpts struct {
	enveloped  bool   // wrap the response in {"status":"ok","results": ...}
	idField    string // identity field injected into the stored object (default "id")
	stringID   bool   // render the assigned id as a JSON string rather than a number
	resultsMap bool   // shape results as {id: obj} (themes_to_dict_map convention)
	upsert     bool   // create POSTs to an instance path with a config-supplied id
	listCreate bool   // create response is a list the provider selects from (collection-body-id)
	// singletonGet models a per-project singleton (business_context, settings):
	// the collection GET returns the single stored object directly (or an empty
	// object when nothing is stored yet), never a list — matching APIResponse
	// views that return results={...} for the project's one settings row.
	singletonGet bool
	// createIDField, when set and different from idField, is the field name the
	// CREATE response carries the server-assigned id under. A read_after_create
	// entity whose create response is a flat id-bearing object can return the id
	// under a key ("id") that differs from the canonical read identity field
	// (e.g. behavior_id): the create handler extracts the id from this key, then
	// re-reads via the instance GET (which returns the id under idField).
	createIDField string
	// createDefaults are server-assigned defaults merged into a created object
	// when the request body does not carry them (e.g. an experiment is born
	// with status "draft", a feature flag with status "disabled").
	createDefaults map[string]any
	// rpcLifecycle models an org-scoped RPC entity (project) that has no REST CRUD:
	// create POSTs {createNameKey:[<name>]} to .../create-<plural>/ and the
	// enveloped response is an ARRAY of created rows (each {idField, matchAttr:name});
	// read GETs the list path and returns every stored row; delete POSTs
	// {idListKey:[<id>]} to .../delete-<plural>/. matchAttr defaults to "name".
	rpcLifecycle  bool
	createNameKey string
	idListKey     string
	matchAttr     string
	// derive, when set, rewrites a stored object after every create and update,
	// the way the server computes some fields from others (a bookmark's
	// metadata carries its name and description).
	derive func(obj map[string]any)
}

// capturedRequest records one body-bearing request the mock served, so tests
// can assert on the EXACT wire body the provider sent (e.g. that an update
// body contains only allowlisted keys).
type capturedRequest struct {
	method string
	path   string
	body   map[string]any
}

// mockServer is an in-memory echo backend for the Mixpanel App API.
type mockServer struct {
	*httptest.Server
	opts    mockOpts
	mu      sync.Mutex
	store   map[string]map[string]any
	counter int
	// captured records every body-bearing request in arrival order (appended by
	// parseBody). Read it via requests().
	captured []capturedRequest
	// shares holds shared-entities project shares keyed by
	// "<entity_type>/<entity_id>" -> project id -> canEdit. See handleSharedEntities.
	shares map[string]map[string]bool
}

// requests returns a copy of the captured body-bearing requests, optionally
// filtered by method ("" matches all).
func (m *mockServer) requests(method string) []capturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []capturedRequest{}
	for _, cr := range m.captured {
		if method == "" || cr.method == method {
			out = append(out, cr)
		}
	}
	return out
}

// newMockServer starts an echo server and registers cleanup. idField defaults to
// "id" when empty.
func newMockServer(t *testing.T, opts mockOpts) *mockServer {
	t.Helper()
	if opts.idField == "" {
		opts.idField = "id"
	}
	m := &mockServer{opts: opts, store: map[string]map[string]any{}, counter: 1000, shares: map[string]map[string]bool{}}
	m.Server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.Close)
	return m
}

func (m *mockServer) handle(w http.ResponseWriter, r *http.Request) {
	// Workspace resolution: feature-flag-style routes resolve the project's
	// canonical workspace via GET .../workspaces before their CRUD calls.
	if r.Method == http.MethodGet && strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/workspaces") {
		writeJSON(w, map[string]any{
			"status": "ok",
			"results": []any{
				map[string]any{"id": 1.0, "is_global": true, "is_default": true, "name": "All Project Data"},
			},
		})
		return
	}

	// Shared-entities routes (project sharing; see sharing.go). Handled before
	// the generic CRUD switch so POST .../upsert and .../delete are not
	// mistaken for entity creates. Additive: entity CRUD is unaffected.
	if idx := strings.Index(r.URL.Path, "/shared-entities/"); idx >= 0 {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.handleSharedEntities(w, r, r.URL.Path[idx+len("/shared-entities/"):])
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.opts.rpcLifecycle {
		m.handleRPC(w, r)
		return
	}

	// Dashboards instance PATCH routes, additive: they model the board content
	// API (create/delete a board report bookmark, which the bookmark resource
	// uses for BOARDS_DASHBOARD_BOOKMARK_TYPES) and the layout write format
	// (stored back in the GET shape so provider refreshes exercise the
	// read-shape transform). Only PATCHes to a .../dashboards/{id} path are
	// intercepted; every other entity still hits the generic CRUD switch.
	// Caller must NOT hold m.mu here; we do (locked above).
	if r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/dashboards/") {
		m.handleDashboardPatch(w, r)
		return
	}

	// Lifecycle verb routes (feature flags + experiments), additive: they model
	// POST/DELETE {id}/archive, PUT {id}/launch, PUT|POST {id}/force_conclude
	// and PATCH {id}/decide so desired_state transition tests can run against
	// the mock. Handled before the generic CRUD switch so a POST to
	// .../{id}/archive is not mistaken for an entity create. Caller holds m.mu.
	if verb, id, ok := lifecycleVerb(r.URL.Path); ok {
		m.handleLifecycleVerb(w, r, verb, id)
		return
	}

	switch r.Method {
	case http.MethodPost:
		body := m.parseBody(r)
		var idStr string
		if seg := lastSegment(r.URL.Path); !m.opts.listCreate {
			if _, exists := m.store[seg]; exists {
				// POST to an existing instance id is an in-place update (some
				// endpoints use POST, not PUT/PATCH, as their update verb, e.g.
				// data_group). Merge into the stored object under the same id so the
				// object is mutated rather than duplicated under a fresh id -- which
				// matches the real API and keeps the post-update id stable for the
				// import round-trip.
				obj := m.store[seg]
				for k, v := range body {
					obj[k] = v
				}
				obj[m.opts.idField] = m.idValue(seg)
				m.store[seg] = obj
				m.respond(w, seg, obj)
				return
			}
		}
		if m.opts.upsert {
			// Upsert: the id is supplied by the configuration and templated into
			// the POST path (create-to-instance), not assigned by the server.
			idStr = lastSegment(r.URL.Path)
		} else {
			m.counter++
			idStr = strconv.Itoa(m.counter)
		}
		body[m.opts.idField] = m.idValue(idStr)
		if m.opts.createIDField != "" && m.opts.createIDField != m.opts.idField {
			// read_after_create entities whose create response carries the id under a
			// different key than the read identity field (e.g. behavior: create -> id,
			// read -> behavior_id). Both keys are harmless on the stored object.
			body[m.opts.createIDField] = m.idValue(idStr)
		}
		for k, v := range m.opts.createDefaults {
			if _, ok := body[k]; !ok {
				body[k] = v
			}
		}
		m.store[idStr] = body
		if m.opts.listCreate {
			// Collection-body-id create: the provider selects the new element from
			// a returned list rather than reading a single object.
			m.respondValue(w, []any{body})
			return
		}
		m.respond(w, idStr, body)
	case http.MethodGet:
		id := lastSegment(r.URL.Path)
		if obj, ok := m.store[id]; ok {
			m.respond(w, id, obj)
			return
		}
		if m.opts.singletonGet {
			// Per-project singleton: the collection GET returns the one stored
			// object (or an empty object before first write), never a list.
			for _, o := range m.store {
				m.respondValue(w, o)
				return
			}
			m.respondValue(w, map[string]any{})
			return
		}
		// Collection GET (read-from-list entities): return every stored object.
		list := make([]any, 0, len(m.store))
		for _, o := range m.store {
			list = append(list, o)
		}
		m.respondValue(w, list)
	case http.MethodPut, http.MethodPatch:
		id := lastSegment(r.URL.Path)
		obj, ok := m.store[id]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		for k, v := range m.parseBody(r) {
			obj[k] = v
		}
		obj[m.opts.idField] = m.idValue(id)
		if m.opts.derive != nil {
			m.opts.derive(obj)
		}
		m.store[id] = obj
		m.respond(w, id, obj)
	case http.MethodDelete:
		delete(m.store, lastSegment(r.URL.Path))
		m.respond(w, "", map[string]any{})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleDashboardPatch answers PATCH .../dashboards/{id} the way the real API
// behaves (live-verified 2026-07-03):
//
//   - body {"content":{"action":"create","content_type":"report",
//     "content_params":{"bookmark":{...}}}} creates the bookmark (stored so
//     GET .../bookmarks/{id} serves it, params kept as the STRING the
//     provider sent — matching the live GET), grows the dashboard's
//     GET-shape layout by one row+cell, and echoes the full dashboard with
//     `new_content` whose params are REWRITTEN into an object (the live
//     server normalizes params in the content-create echo).
//   - body {"content":{"action":"delete","content_id":N}} removes the stored
//     bookmark and its layout cell(s); repeating the delete is a 200 no-op.
//   - a body carrying "layout" in the WRITE format ({"rows":[...],
//     "rows_order":[...]}) is converted to the GET shape ({"rows":{id:row},
//     "order":[...],"version":"2.0.0"}, cells enriched with content
//     pointers) before storing, so refreshes exercise the provider's
//     read-shape transform.
//   - anything else merges like the generic PATCH.
//
// Caller holds m.mu.
func (m *mockServer) handleDashboardPatch(w http.ResponseWriter, r *http.Request) {
	id := lastSegment(r.URL.Path)
	obj, ok := m.store[id]
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	body := m.parseBody(r)
	if content, ok := body["content"].(map[string]any); ok {
		m.handleDashboardContent(w, id, obj, content)
		return
	}
	if lay, ok := body["layout"].(map[string]any); ok {
		if converted, ok := mockLayoutGetShapeFromWrite(lay); ok {
			body["layout"] = converted
		}
	}
	for k, v := range body {
		obj[k] = v
	}
	obj[m.opts.idField] = m.idValue(id)
	m.store[id] = obj
	m.respond(w, id, obj)
}

// handleDashboardContent models the board content actions. Caller holds m.mu.
func (m *mockServer) handleDashboardContent(w http.ResponseWriter, dashID string, dash map[string]any, content map[string]any) {
	action, _ := content["action"].(string)
	switch action {
	case "create":
		cp, _ := content["content_params"].(map[string]any)
		bm, _ := cp["bookmark"].(map[string]any)
		if bm == nil {
			http.Error(w, `{"status":"error","error":"content_params.bookmark required"}`, http.StatusBadRequest)
			return
		}
		m.counter++
		idStr := strconv.Itoa(m.counter)
		stored := map[string]any{}
		for k, v := range bm {
			stored[k] = v
		}
		stored[m.opts.idField] = m.idValue(idStr)
		stored["dashboard_id"] = m.idValue(dashID)
		if m.opts.derive != nil {
			m.opts.derive(stored)
		}
		m.store[idStr] = stored
		// new_content echoes the bookmark with params rewritten into an object.
		echo := map[string]any{}
		for k, v := range stored {
			echo[k] = v
		}
		if s, ok := stored["params"].(string); ok {
			var decoded map[string]any
			if json.Unmarshal([]byte(s), &decoded) == nil && decoded != nil {
				decoded["__mock_server_rewritten"] = true
				echo["params"] = decoded
			}
		}
		layout := mockDashboardLayoutOf(dash)
		rows, _ := layout["rows"].(map[string]any)
		rowID := "row" + idStr
		rows[rowID] = map[string]any{
			"height": 0.0,
			"cells": []any{map[string]any{
				"id": "cell" + idStr, "width": 12.0,
				"content_id": m.idValue(idStr), "content_type": "report",
			}},
		}
		layout["order"] = append(mockAnySlice(layout["order"]), rowID)
		dash["layout"] = layout
		m.store[dashID] = dash
		respObj := map[string]any{}
		for k, v := range dash {
			respObj[k] = v
		}
		respObj["new_content"] = echo
		m.respondValue(w, respObj)
	case "delete":
		cid := idToKey(content["content_id"])
		delete(m.store, cid)
		layout := mockDashboardLayoutOf(dash)
		rows, _ := layout["rows"].(map[string]any)
		newOrder := []any{}
		for _, ro := range mockAnySlice(layout["order"]) {
			rid, _ := ro.(string)
			row, _ := rows[rid].(map[string]any)
			if row == nil {
				continue
			}
			kept := []any{}
			for _, c := range mockAnySlice(row["cells"]) {
				cm, _ := c.(map[string]any)
				if cm != nil && idToKey(cm["content_id"]) == cid {
					continue
				}
				kept = append(kept, c)
			}
			if len(kept) == 0 {
				delete(rows, rid)
				continue
			}
			row["cells"] = kept
			newOrder = append(newOrder, rid)
		}
		layout["order"] = newOrder
		dash["layout"] = layout
		m.store[dashID] = dash
		m.respondValue(w, dash)
	default:
		http.Error(w, `{"status":"error","error":"unsupported content action"}`, http.StatusBadRequest)
	}
}

// mockDashboardLayoutOf returns the dashboard's GET-shape layout, creating an
// empty one ({"rows":{},"order":[],"version":"2.0.0"}) when absent.
func mockDashboardLayoutOf(dash map[string]any) map[string]any {
	if l, ok := dash["layout"].(map[string]any); ok {
		if _, ok := l["rows"].(map[string]any); ok {
			return l
		}
	}
	l := map[string]any{"rows": map[string]any{}, "order": []any{}, "version": "2.0.0"}
	dash["layout"] = l
	return l
}

// mockLayoutGetShapeFromWrite converts a layout WRITE body into the GET shape
// the real dashboards GET returns (rows dict keyed by row id, order list,
// version string, cells enriched with content pointers).
func mockLayoutGetShapeFromWrite(lay map[string]any) (map[string]any, bool) {
	rowsList, ok := lay["rows"].([]any)
	if !ok {
		return nil, false
	}
	rows := map[string]any{}
	order := []any{}
	if ro, ok := lay["rows_order"].([]any); ok {
		order = ro
	}
	for _, rr := range rowsList {
		rm, ok := rr.(map[string]any)
		if !ok {
			continue
		}
		rid, _ := rm["id"].(string)
		if rid == "" {
			rid, _ = rm["temp_id"].(string)
		}
		cells := []any{}
		for _, c := range mockAnySlice(rm["cells"]) {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			cells = append(cells, map[string]any{
				"id": cm["id"], "width": cm["width"],
				"content_id": nil, "content_type": "report",
			})
		}
		rows[rid] = map[string]any{"height": rm["height"], "cells": cells}
	}
	return map[string]any{"rows": rows, "order": order, "version": "2.0.0"}, true
}

func mockAnySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// lifecycleVerb recognizes lifecycle verb paths ({id}/archive, {id}/launch,
// {id}/force_conclude, {id}/decide) and returns the verb and instance id.
func lifecycleVerb(p string) (verb, id string, ok bool) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) < 2 {
		return "", "", false
	}
	switch last := segs[len(segs)-1]; last {
	case "archive", "launch", "force_conclude", "decide":
		return last, segs[len(segs)-2], true
	}
	return "", "", false
}

// handleLifecycleVerb models the feature-flag / experiment lifecycle verbs the
// way the real API behaves (live-verified 2026-07-02):
//
//   - POST {id}/archive soft-deletes (sets `deleted`); refuses an enabled flag
//     (400 CannotDeleteEnabledFlag) and auto-concludes an active experiment.
//   - DELETE {id}/archive restores (clears `deleted`).
//   - PUT {id}/launch: draft/active/concluded -> active (+start_date); a
//     decided experiment (success/fail) is rejected (ExperimentAlreadyStarted).
//   - PUT|POST {id}/force_conclude: active -> concluded (+end_date); a no-op
//     from any other status, matching the server's tolerated
//     ExperimentAlreadyConcluded.
//   - PATCH {id}/decide: sets status success/fail from the body's `success`.
//
// Caller holds m.mu.
func (m *mockServer) handleLifecycleVerb(w http.ResponseWriter, r *http.Request, verb, id string) {
	obj, ok := m.store[id]
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	status, _ := obj["status"].(string)
	switch verb {
	case "archive":
		switch r.Method {
		case http.MethodPost:
			if status == "enabled" {
				http.Error(w, `{"status":"error","error":"Unable to delete enabled feature flag","type":"CannotDeleteEnabledFlag"}`, http.StatusBadRequest)
				return
			}
			if status == "active" {
				obj["status"] = "concluded"
				obj["end_date"] = "2026-01-01T00:00:00"
			}
			obj["deleted"] = "2026-01-01T00:00:00"
			m.store[id] = obj
			m.respondValue(w, map[string]any{})
		case http.MethodDelete:
			obj["deleted"] = nil
			m.store[id] = obj
			m.respond(w, id, obj)
		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	case "launch":
		if r.Method != http.MethodPut {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		switch status {
		case "", "draft", "active", "concluded":
			obj["status"] = "active"
			if obj["start_date"] == nil {
				obj["start_date"] = "2026-01-01T00:00:00"
			}
			m.store[id] = obj
			m.respond(w, id, obj)
		default:
			http.Error(w, `{"status":"error","error":"ExperimentAlreadyStarted"}`, http.StatusBadRequest)
		}
	case "force_conclude":
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if status == "active" {
			obj["status"] = "concluded"
			obj["end_date"] = "2026-01-01T00:00:00"
		}
		m.store[id] = obj
		m.respond(w, id, obj)
	case "decide":
		if r.Method != http.MethodPatch {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		body := m.parseBody(r)
		if s, _ := body["success"].(bool); s {
			obj["status"] = "success"
		} else {
			obj["status"] = "fail"
		}
		if obj["end_date"] == nil {
			obj["end_date"] = "2026-01-01T00:00:00"
		}
		m.store[id] = obj
		m.respond(w, id, obj)
	}
}

// handleSharedEntities models the shared-entities API (always enveloped,
// regardless of the entity's own envelope contract — matching the real API,
// verified live 2026-07-02):
//
//	POST .../shared-entities/{type}/{id}/upsert  {"id":e,"projectShares":[{"id":p,"canEdit":b}]}
//	POST .../shared-entities/{type}/{id}/delete  {"id":e,"projectShares":[p]}
//	GET  .../shared-entities/{type}/{id}         -> results.projectShares
//
// rest is the path remainder after "/shared-entities/". Caller holds m.mu.
func (m *mockServer) handleSharedEntities(w http.ResponseWriter, r *http.Request, rest string) {
	segs := strings.Split(strings.Trim(rest, "/"), "/")
	if len(segs) < 2 {
		http.Error(w, `{"status":"error","error":"bad shared-entities path"}`, http.StatusNotFound)
		return
	}
	key := segs[0] + "/" + segs[1]
	action := ""
	if len(segs) > 2 {
		action = segs[2]
	}
	switch {
	case r.Method == http.MethodPost && action == "upsert":
		body := m.parseBody(r)
		shares, _ := body["projectShares"].([]any)
		if m.shares[key] == nil {
			m.shares[key] = map[string]bool{}
		}
		for _, s := range shares {
			obj, ok := s.(map[string]any)
			if !ok {
				continue
			}
			canEdit, _ := obj["canEdit"].(bool)
			m.shares[key][idToKey(obj["id"])] = canEdit
		}
		m.respondShares(w, key, segs[1])
	case r.Method == http.MethodPost && action == "delete":
		body := m.parseBody(r)
		ids, _ := body["projectShares"].([]any)
		for _, id := range ids {
			delete(m.shares[key], idToKey(id))
		}
		m.respondShares(w, key, segs[1])
	case r.Method == http.MethodGet && action == "":
		m.respondShares(w, key, segs[1])
	default:
		http.Error(w, `{"status":"error","error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// respondShares writes the shared-entities dict for one entity (always
// enveloped, like the real endpoint).
func (m *mockServer) respondShares(w http.ResponseWriter, key, entityID string) {
	projectShares := make([]any, 0, len(m.shares[key]))
	for pid, canEdit := range m.shares[key] {
		var idVal any = pid
		if f, err := strconv.ParseFloat(pid, 64); err == nil {
			idVal = f
		}
		projectShares = append(projectShares, map[string]any{"id": idVal, "canEdit": canEdit})
	}
	writeJSON(w, map[string]any{
		"status": "ok",
		"results": map[string]any{
			"id":            entityID,
			"userShares":    []any{},
			"teamShares":    []any{},
			"projectShares": projectShares,
		},
	})
}

// handleRPC answers the org-scoped RPC lifecycle (project): create-<plural>,
// the list path, and delete-<plural>. The caller holds m.mu.
func (m *mockServer) handleRPC(w http.ResponseWriter, r *http.Request) {
	matchAttr := m.opts.matchAttr
	if matchAttr == "" {
		matchAttr = "name"
	}
	seg := lastSegment(r.URL.Path)
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(seg, "create-"):
		body := m.parseBody(r)
		names, _ := body[m.opts.createNameKey].([]any)
		created := make([]any, 0, len(names))
		for _, n := range names {
			m.counter++
			idStr := strconv.Itoa(m.counter)
			obj := map[string]any{m.opts.idField: m.idValue(idStr), matchAttr: n}
			m.store[idStr] = obj
			created = append(created, obj)
		}
		m.respondValue(w, created)
	case r.Method == http.MethodPost && strings.HasPrefix(seg, "delete-"):
		body := m.parseBody(r)
		ids, _ := body[m.opts.idListKey].([]any)
		for _, id := range ids {
			delete(m.store, idToKey(id))
		}
		m.respondValue(w, map[string]any{})
	case r.Method == http.MethodGet:
		// List path: return every stored row (read_from_list selects by id).
		list := make([]any, 0, len(m.store))
		for _, o := range m.store {
			list = append(list, o)
		}
		m.respondValue(w, list)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// mutateStored applies fn to every stored object under the store lock,
// simulating an OUT-OF-BAND edit (a webapp user changing the entity) between
// test steps. Tests run it from a TestStep PreConfig so the step's refresh
// observes the drifted server state; a subsequent RefreshState step with
// ExpectNonEmptyPlan then proves the provider surfaces the drift.
func (m *mockServer) mutateStored(fn func(id string, obj map[string]any)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, obj := range m.store {
		fn(id, obj)
	}
}

// idToKey renders a JSON-decoded id (number or string) as the store key, matching
// the decimal string strconv.Itoa produced when the row was created.
func idToKey(id any) string {
	switch x := id.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// idValue renders the assigned id as the type the schema expects.
func (m *mockServer) idValue(idStr string) any {
	if m.opts.stringID {
		return idStr
	}
	f, _ := strconv.ParseFloat(idStr, 64)
	return f
}

// parseBody decodes a JSON or form-urlencoded request body into a map. Form
// fields are JSON-decoded when possible (the form convention used by a few legacy
// endpoints, where lists/objects arrive as JSON-encoded form values).
func (m *mockServer) parseBody(r *http.Request) map[string]any {
	out := map[string]any{}
	if strings.Contains(r.Header.Get("Content-Type"), "form-urlencoded") {
		_ = r.ParseForm()
		for k, vs := range r.PostForm {
			if len(vs) == 0 {
				continue
			}
			var v any
			if err := json.Unmarshal([]byte(vs[0]), &v); err == nil {
				out[k] = v
			} else {
				out[k] = vs[0]
			}
		}
		m.captured = append(m.captured, capturedRequest{method: r.Method, path: r.URL.Path, body: out})
		return out
	}
	_ = json.NewDecoder(r.Body).Decode(&out)
	// Caller (handle / handleSharedEntities / handleRPC) holds m.mu.
	m.captured = append(m.captured, capturedRequest{method: r.Method, path: r.URL.Path, body: out})
	return out
}

// respond writes the entity body, applying the results-map and envelope shapes.
func (m *mockServer) respond(w http.ResponseWriter, idStr string, obj map[string]any) {
	if m.opts.resultsMap && idStr != "" {
		m.respondValue(w, map[string]any{idStr: obj})
		return
	}
	m.respondValue(w, obj)
}

// respondValue writes an arbitrary results value, enveloped when configured.
func (m *mockServer) respondValue(w http.ResponseWriter, results any) {
	if m.opts.enveloped {
		writeJSON(w, map[string]any{"status": "ok", "results": results})
		return
	}
	writeJSON(w, results)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// lastSegment returns the final non-empty path segment (the instance id).
func lastSegment(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	return parts[len(parts)-1]
}

// providerConfig renders the provider block (pointed at the mock) plus the given
// resource/data-source HCL.
func providerConfig(baseURL, body string) string {
	return fmt.Sprintf(`
provider "mixpanel" {
  service_account        = "test"
  service_account_secret = "test"
  project_id             = "1"
  organization_id        = "1"
  base_url               = %q
}
`, baseURL) + body
}
