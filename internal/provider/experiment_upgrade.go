// HAND-EDITED (not generated): state upgrade for the "metrics" HAND-EDITED
// EXCEPTION in experiment_spec.go. Re-apply if regenerating.

package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// UpgradeState moves state saved before v0.1.6, where `metrics` was the
// generated nested list, to the jsonencode string. The old value is dropped:
// `metrics` is Computed, so the refresh that follows the upgrade reads the
// experiment's metrics back from Mixpanel.
func (r *ExperimentResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: r.upgradeStateV0},
	}
}

func (r *ExperimentResource) upgradeStateV0(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.RawState == nil {
		resp.Diagnostics.AddError("Missing prior state", "the experiment's saved state is empty")
		return
	}
	var sresp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sresp)
	val, err := upgradeExperimentRawStateV0(sresp.Schema.Type().TerraformType(ctx), req.RawState)
	if err != nil {
		resp.Diagnostics.AddError("Unable to upgrade experiment state", err.Error())
		return
	}
	resp.State = tfsdk.State{Schema: sresp.Schema, Raw: val}
}

func upgradeExperimentRawStateV0(typ tftypes.Type, raw *tfprotov6.RawState) (tftypes.Value, error) {
	var m map[string]any
	if err := json.Unmarshal(raw.JSON, &m); err != nil {
		return tftypes.Value{}, err
	}
	m["metrics"] = nil
	b, err := json.Marshal(m)
	if err != nil {
		return tftypes.Value{}, err
	}
	upgraded := tfprotov6.RawState{JSON: b}
	return upgraded.UnmarshalWithOpts(typ, tfprotov6.UnmarshalOpts{
		ValueFromJSONOpts: tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true},
	})
}
