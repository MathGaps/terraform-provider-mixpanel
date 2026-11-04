// HAND-EDITED (not generated): state upgrade for HAND-EDITED EXCEPTION 3 in
// dashboard_spec.go. Re-apply if regenerating.

package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// UpgradeState moves state saved before v0.1.2, where `filters` was the
// generated nested list, to the jsonencode string. The old value is dropped:
// `filters` is Computed, so the refresh that follows the upgrade reads the
// board's filters back from Mixpanel.
func (r *DashboardResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: r.upgradeStateV0},
	}
}

func (r *DashboardResource) upgradeStateV0(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.RawState == nil {
		resp.Diagnostics.AddError("Missing prior state", "the dashboard's saved state is empty")
		return
	}
	var sresp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sresp)
	val, err := upgradeDashboardRawStateV0(ctx, sresp.Schema.Type().TerraformType(ctx), req.RawState)
	if err != nil {
		resp.Diagnostics.AddError("Unable to upgrade dashboard state", err.Error())
		return
	}
	resp.State = tfsdk.State{Schema: sresp.Schema, Raw: val}
}

func upgradeDashboardRawStateV0(ctx context.Context, typ tftypes.Type, raw *tfprotov6.RawState) (tftypes.Value, error) {
	var m map[string]any
	if err := json.Unmarshal(raw.JSON, &m); err != nil {
		return tftypes.Value{}, err
	}
	m["filters"] = nil
	b, err := json.Marshal(m)
	if err != nil {
		return tftypes.Value{}, err
	}
	upgraded := tfprotov6.RawState{JSON: b}
	return upgraded.UnmarshalWithOpts(typ, tfprotov6.UnmarshalOpts{
		ValueFromJSONOpts: tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true},
	})
}
