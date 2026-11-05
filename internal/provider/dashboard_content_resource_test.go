package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// boardContentServer answers one board's content PATCH and GET the way
// Mixpanel does (live-verified 2026-11-05): create returns new_content and adds
// a layout cell; update and delete name the card by content_id; the GET carries
// results.contents[<type>][<id>] with the server's own fields added.
type boardContentServer struct {
	mu     sync.Mutex
	next   int
	cards  map[string]map[string]any // id -> card
	cells  map[string]string         // id -> cell id
	ctypes map[string]string         // id -> content type
}

func newBoardContentServer(t *testing.T) (*httptest.Server, *boardContentServer) {
	b := &boardContentServer{next: 500, cards: map[string]map[string]any{}, cells: map[string]string{}, ctypes: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/dashboards/77") {
			http.NotFound(w, r)
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			var req struct {
				Content struct {
					Action        string         `json:"action"`
					ContentType   string         `json:"content_type"`
					ContentID     any            `json:"content_id"`
					ContentParams map[string]any `json:"content_params"`
				} `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			c := req.Content
			results := b.board()
			switch c.Action {
			case "create":
				b.next++
				id := fmt.Sprint(b.next)
				card := map[string]any{"id": b.next, "dashboard_id": 77, "creator": "sa"}
				for k, v := range c.ContentParams {
					card[k] = v
				}
				b.cards[id], b.cells[id], b.ctypes[id] = card, "cell"+id, c.ContentType
				results = b.board()
				results["new_content"] = card
			case "update":
				id := fmt.Sprint(c.ContentID)
				for k, v := range c.ContentParams {
					b.cards[id][k] = v
				}
			case "delete":
				id := fmt.Sprint(c.ContentID)
				delete(b.cards, id)
				delete(b.cells, id)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "results": results})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "results": b.board()})
	}))
	t.Cleanup(srv.Close)
	return srv, b
}

func (b *boardContentServer) board() map[string]any {
	contents := map[string]any{"report": map[string]any{}, "text": map[string]any{}, "iframe": map[string]any{}}
	var cells []any
	for id, card := range b.cards {
		contents[b.ctypes[id]].(map[string]any)[id] = card
		cells = append(cells, map[string]any{"id": b.cells[id], "width": 12, "content_id": card["id"], "content_type": b.ctypes[id]})
	}
	return map[string]any{"id": 77, "contents": contents,
		"layout": map[string]any{"rows": map[string]any{"r1": map[string]any{"height": 0, "cells": cells}}, "order": []any{"r1"}}}
}

func textCardConfig(url, markdown string) string {
	return providerConfig(url, fmt.Sprintf(`
resource "mixpanel_dashboard_content" "intro" {
  dashboard_id = "77"
  content_type = "text"
  content      = jsonencode({ markdown = %q })
}`, markdown))
}

func TestAccDashboardContent_textCardLifecycle(t *testing.T) {
	srv, b := newBoardContentServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProtoV6,
		Steps: []resource.TestStep{
			{
				// Created on the board with its cell; the server's own fields
				// (creator, dashboard_id) do not show as drift.
				Config: textCardConfig(srv.URL, "<h2>How to read this</h2>"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("mixpanel_dashboard_content.intro", "id", "501"),
					resource.TestCheckResourceAttr("mixpanel_dashboard_content.intro", "cell_id", "cell501"),
				),
			},
			{
				// A new text is an update in place, not a new card.
				Config: textCardConfig(srv.URL, "<h2>Where people drop</h2>"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mixpanel_dashboard_content.intro", plancheck.ResourceActionUpdate),
				}},
			},
			{
				// An edit made in Mixpanel shows as drift against the config.
				PreConfig: func() {
					b.mu.Lock()
					b.cards["501"]["markdown"] = "<h2>edited by hand</h2>"
					b.mu.Unlock()
				},
				Config:             textCardConfig(srv.URL, "<h2>Where people drop</h2>"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
		CheckDestroy: func(_ *terraform.State) error {
			if len(b.cards) != 0 {
				return fmt.Errorf("cards left on the board after destroy: %v", b.cards)
			}
			return nil
		},
	})
}
