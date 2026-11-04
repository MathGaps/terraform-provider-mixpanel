package provider

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// State saved by v0.1.1 holds `filters` as the generated nested list; v0.1.2
// reads it as a string. The upgrade must load it and keep everything else.
func TestDashboardUpgradeStateV0(t *testing.T) {
	ctx := context.Background()
	r := &DashboardResource{}
	raw := &tfprotov6.RawState{JSON: []byte(`{
		"id": "11603813", "project_id": "2519332", "title": "Refer & Earn",
		"filters": [{"resource_type": "user", "filter_operator": "equals"}],
		"attribute_removed_since": "x"
	}`)}
	var resp resource.UpgradeStateResponse
	r.upgradeStateV0(ctx, resource.UpgradeStateRequest{RawState: raw}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrade failed: %v", resp.Diagnostics)
	}
	var title types.String
	var filters jsontypes.Normalized
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("title"), &title)...)
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("filters"), &filters)...)
	if resp.Diagnostics.HasError() {
		t.Fatalf("reading upgraded state: %v", resp.Diagnostics)
	}
	if title.ValueString() != "Refer & Earn" {
		t.Errorf("title = %q, want the saved title", title.ValueString())
	}
	if !filters.IsNull() {
		t.Errorf("filters = %v, want null until the refresh reads it", filters)
	}
}
