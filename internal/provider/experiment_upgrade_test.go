package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// State saved by v0.1.5 holds `metrics` as the generated nested list; v0.1.6
// reads it as a string. The upgrade must load it and keep everything else.
func TestExperimentUpgradeStateV0(t *testing.T) {
	ctx := context.Background()
	r := &ExperimentResource{}
	raw := &tfprotov6.RawState{JSON: []byte(`{
		"id": "18c60edd-ec1d-4705-b155-ea9774b27d61", "project_id": 2353483,
		"name": "onboarding-questions", "desired_state": "active",
		"metrics": [{"id": "m1", "name": "resource_created", "type": "primary", "data_group_id": ""}],
		"variants": [{"id": "asked", "key": "asked", "name": "asked"}],
		"attribute_removed_since": "x"
	}`)}
	var resp resource.UpgradeStateResponse
	r.upgradeStateV0(ctx, resource.UpgradeStateRequest{RawState: raw}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrade failed: %v", resp.Diagnostics)
	}
	var name, state types.String
	var metrics jsontypes.Normalized
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("name"), &name)...)
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("desired_state"), &state)...)
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("metrics"), &metrics)...)
	if resp.Diagnostics.HasError() {
		t.Fatalf("reading upgraded state: %v", resp.Diagnostics)
	}
	if name.ValueString() != "onboarding-questions" || state.ValueString() != "active" {
		t.Errorf("name, desired_state = %q, %q; want the saved values", name.ValueString(), state.ValueString())
	}
	if !metrics.IsNull() {
		t.Errorf("metrics = %v, want null until the refresh reads it", metrics)
	}
}
