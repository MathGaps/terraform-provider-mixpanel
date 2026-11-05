# A text card: Mixpanel's markdown field takes the board editor's HTML.
resource "mixpanel_dashboard_content" "intro" {
  dashboard_id = mixpanel_dashboard.board.id
  content_type = "text"
  content = jsonencode({
    markdown = "<h2>How to read this board</h2><p>Top row: the rates that matter.</p>"
  })
}

# An embedded page.
resource "mixpanel_dashboard_content" "status" {
  dashboard_id = mixpanel_dashboard.board.id
  content_type = "iframe"
  content      = jsonencode({ url = "https://status.example.com", title = "Status" })
}
