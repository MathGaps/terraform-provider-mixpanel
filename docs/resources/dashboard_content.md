---
page_title: "mixpanel_dashboard_content Resource - mixpanel"
subcategory: "Analytics & Reporting"
description: |-
  Manages a non-report card on a board: a text card, an embedded page, a media card.
---

# mixpanel_dashboard_content (Resource)

Manages any card on a [board](dashboard.md) that is not a report: `text`,
`iframe`, `media` and the other content types Mixpanel's board has. Reports are
[mixpanel_bookmark](bookmark.md).

`content` is Mixpanel's own `content_params` for the type, as
`jsonencode({...})`, passed through as written, so every field the board editor
can set is reachable: `markdown` for `text` (the editor's HTML), `url` and
`title` for `iframe`. Creating a card adds its cell to the board's layout;
`cell_id` names that cell, for `mixpanel_dashboard.layout`.

Drift is read on the keys `content` sets: an edit made in Mixpanel to one of
them shows in the next plan, and the server's own fields (`creator`,
`signed_url`, …) never do.

Writes to one board are made one at a time, because Mixpanel rewrites the
board's layout on each and refuses a concurrent one (`INVALID_LAYOUT`).

## Example Usage

```terraform
resource "mixpanel_dashboard_content" "intro" {
  dashboard_id = mixpanel_dashboard.board.id
  content_type = "text"
  content = jsonencode({
    markdown = "<h2>How to read this board</h2><p>Top row: the rates that matter.</p>"
  })
}

resource "mixpanel_dashboard_content" "status" {
  dashboard_id = mixpanel_dashboard.board.id
  content_type = "iframe"
  content      = jsonencode({ url = "https://status.example.com", title = "Status" })
}
```

## Schema

### Required

- `dashboard_id` (String) The board the card is on. Changing it replaces the card.
- `content_type` (String) `text`, `iframe`, `media`, … (`report` is `mixpanel_bookmark`). Changing it replaces the card.
- `content` (String) The card's `content_params` as JSON.

### Optional

- `project_id` (String) Defaults to the provider's.

### Read-Only

- `id` (String) The card's id: a number for `text`, a UUID for `iframe`.
- `cell_id` (String) The card's cell in the board layout.

## Import

```shell
terraform import mixpanel_dashboard_content.intro 1234567:7654321:text:12363559
```
