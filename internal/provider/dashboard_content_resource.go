// mixpanel_dashboard_content: any card on a board that is not a report — a text
// card, an embedded page (iframe), a media card, … — through the one route
// Mixpanel gives them, the dashboards content PATCH (live-verified 2026-11-05
// against project 2519332):
//
//	PATCH /api/app/projects/{p}/dashboards/{d}
//	{"content":{"action":"create","content_type":"text","content_params":{"markdown":"<h2>…</h2>"}}}
//	  -> results.new_content {"id":12363559,"markdown":…}; a layout row+cell is made with it
//	{"content":{"action":"update","content_type":"text","content_id":12363559,"content_params":{…}}}
//	{"content":{"action":"delete","content_type":"text","content_id":12363559}}
//
// and reads back from GET /dashboards/{d} as results.contents[<type>][<id>]. The
// card's own fields are `content` — Mixpanel's content_params, passed through as
// written — so every content type Mixpanel has is reachable without the
// provider naming its fields (text's `markdown`, iframe's `url` and `title`).
// Ids are numbers for some types and UUID strings for others (iframe), so `id`
// is a string. A report is not this resource's: it is a mixpanel_bookmark.
//
// Drift: Read compares only the keys `content` sets; the server's own additions
// (creator, signed_url, …) never show as a diff, and a changed value of a key
// you set does.

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mixpanel/terraform-provider-mixpanel/internal/client"
)

var (
	_ resource.Resource                = (*DashboardContentResource)(nil)
	_ resource.ResourceWithConfigure   = (*DashboardContentResource)(nil)
	_ resource.ResourceWithImportState = (*DashboardContentResource)(nil)
)

// NewDashboardContentResource constructs the board-card resource.
func NewDashboardContentResource() resource.Resource { return &DashboardContentResource{} }

type DashboardContentResource struct{ client *client.Client }

type dashboardContentModel struct {
	ID          types.String         `tfsdk:"id"`
	ProjectID   types.String         `tfsdk:"project_id"`
	DashboardID types.String         `tfsdk:"dashboard_id"`
	ContentType types.String         `tfsdk:"content_type"`
	Content     jsontypes.Normalized `tfsdk:"content"`
	CellID      types.String         `tfsdk:"cell_id"`
}

func (r *DashboardContentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard_content"
}

func (r *DashboardContentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A non-report card on a board — `text`, `iframe`, `media`, … — managed through the " +
			"board's content API. `content` is Mixpanel's own content_params for the type, as `jsonencode({...})`: " +
			"`{markdown = \"<h2>…</h2>\"}` for `text`, `{url = \"…\", title = \"…\"}` for `iframe`. Reports are `mixpanel_bookmark`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				MarkdownDescription: "The card's id (a number for `text`, a UUID for `iframe`)."},
			"project_id": schema.StringAttribute{Optional: true, Computed: true,
				PlanModifiers:       append([]planmodifier.String{stringplanmodifier.UseStateForUnknown()}, replace...),
				MarkdownDescription: "Project id; defaults to the provider's."},
			"dashboard_id": schema.StringAttribute{Required: true, PlanModifiers: replace,
				MarkdownDescription: "The board the card is on."},
			"content_type": schema.StringAttribute{Required: true, PlanModifiers: replace,
				Validators:          []validator.String{stringvalidator.NoneOf("report")},
				MarkdownDescription: "Mixpanel's content type: `text`, `iframe`, `media`, … (`report` is `mixpanel_bookmark`)."},
			"content": schema.StringAttribute{Required: true, CustomType: jsontypes.NormalizedType{},
				MarkdownDescription: "The card's content_params as JSON, passed to Mixpanel as written."},
			"cell_id": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				MarkdownDescription: "The card's cell in the board layout, for `mixpanel_dashboard.layout`."},
		},
	}
}

func (r *DashboardContentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *client.Client, got: %T.", req.ProviderData))
		return
	}
	r.client = c
}

func (r *DashboardContentResource) boardPath(projectID, dashboardID string) string {
	return "/api/app/projects/" + projectID + "/dashboards/" + dashboardID
}

// patch sends one content action and returns the unwrapped results.
func (r *DashboardContentResource) patch(ctx context.Context, m dashboardContentModel, action string, params map[string]any) (map[string]any, error) {
	content := map[string]any{"action": action, "content_type": m.ContentType.ValueString()}
	if action != "create" {
		content["content_id"] = jsonNumberOrString(m.ID.ValueString())
	}
	if params != nil {
		content["content_params"] = params
	}
	body, err := dashboardPatchWithConflictRetry(ctx, r.client, r.boardPath(m.ProjectID.ValueString(), m.DashboardID.ValueString()), map[string]any{"content": content})
	if err != nil {
		return nil, err
	}
	return unwrapBody(body, true)
}

func decodeContent(v jsontypes.Normalized) (map[string]any, error) {
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(v.ValueString()))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, fmt.Errorf("content must be a JSON object, as jsonencode({...})")
	}
	// The same number form unwrapBody gives a live card, so the two compare.
	return normalizeNumbers(m).(map[string]any), nil
}

func (r *DashboardContentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m dashboardContentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.ProjectID = types.StringValue(r.client.ProjectID(m.ProjectID.ValueString()))
	params, err := decodeContent(m.Content)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("content"), "Invalid content", err.Error())
		return
	}
	results, err := r.patch(ctx, m, "create", params)
	if err != nil {
		resp.Diagnostics.AddError("Creating board card", err.Error())
		return
	}
	nc, _ := results["new_content"].(map[string]any)
	id, ok := client.IDFromWire(nc, "id")
	if !ok {
		resp.Diagnostics.AddError("Creating board card", "the board's content-create response carries no results.new_content.id")
		return
	}
	m.ID = types.StringValue(id)
	m.CellID = types.StringValue(cellForContent(results["layout"], m.ContentType.ValueString(), id))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *DashboardContentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m dashboardContentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := r.client.Do(ctx, "GET", r.boardPath(m.ProjectID.ValueString(), m.DashboardID.ValueString()), nil)
	if err != nil {
		if apiErr, ok := err.(*client.APIError); ok && apiErr.StatusCode == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Reading board card", err.Error())
		return
	}
	board, err := unwrapBody(body, true)
	if err != nil {
		resp.Diagnostics.AddError("Reading board card", err.Error())
		return
	}
	live, found := liveContent(board, m.ContentType.ValueString(), m.ID.ValueString())
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	next, err := refreshedContent(m.Content, live)
	if err != nil {
		resp.Diagnostics.AddError("Reading board card", err.Error())
		return
	}
	m.Content = next
	m.CellID = types.StringValue(cellForContent(board["layout"], m.ContentType.ValueString(), m.ID.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *DashboardContentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dashboardContentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.ProjectID, plan.CellID = state.ID, state.ProjectID, state.CellID
	params, err := decodeContent(plan.Content)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("content"), "Invalid content", err.Error())
		return
	}
	if _, err := r.patch(ctx, plan, "update", params); err != nil {
		resp.Diagnostics.AddError("Updating board card", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DashboardContentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m dashboardContentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.patch(ctx, m, "delete", nil); err != nil {
		// The board itself is gone, and its cards with it.
		if apiErr, ok := err.(*client.APIError); ok && apiErr.StatusCode == 404 {
			return
		}
		resp.Diagnostics.AddError("Deleting board card", err.Error())
	}
}

// ImportState takes "<project_id>:<dashboard_id>:<content_type>:<id>"; `content`
// is read back whole, so the first plan shows the config's keys against it.
func (r *DashboardContentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, ":", 4)
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("expected \"<project_id>:<dashboard_id>:<content_type>:<id>\", got %q", req.ID))
		return
	}
	m := dashboardContentModel{
		ProjectID: types.StringValue(parts[0]), DashboardID: types.StringValue(parts[1]),
		ContentType: types.StringValue(parts[2]), ID: types.StringValue(parts[3]),
		Content: jsontypes.NewNormalizedValue("{}"), CellID: types.StringUnknown(),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// liveContent finds a card in a board GET's results.contents[<type>].
func liveContent(board map[string]any, contentType, id string) (map[string]any, bool) {
	contents, _ := board["contents"].(map[string]any)
	byID, _ := contents[contentType].(map[string]any)
	card, ok := byID[id].(map[string]any)
	return card, ok
}

// refreshedContent keeps the configured keys, each at its live value. An empty
// object (an import) takes the whole live card minus the server's bookkeeping.
func refreshedContent(current jsontypes.Normalized, live map[string]any) (jsontypes.Normalized, error) {
	want, err := decodeContent(current)
	if err != nil {
		return current, err
	}
	out := map[string]any{}
	if len(want) == 0 {
		for k, v := range live {
			if !serverOwnedCardKeys[k] {
				out[k] = v
			}
		}
	} else {
		for k, v := range want {
			lv, ok := live[k]
			if !ok {
				out[k] = v // a key the server does not echo cannot drift
				continue
			}
			out[k] = lv
		}
	}
	if reflect.DeepEqual(out, want) {
		return current, nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return current, err
	}
	return jsontypes.NewNormalizedValue(string(b)), nil
}

var serverOwnedCardKeys = map[string]bool{
	"id": true, "dashboard_id": true, "creator": true, "creator_id": true, "creator_name": true,
	"creator_email": true, "signed_url": true, "has_signing_key": true,
}

// cellForContent finds the layout cell that holds a card, in either the GET
// shape ({"rows":{id:row},...}) or a list of rows; "" when none does.
func cellForContent(layout any, contentType, id string) string {
	l, _ := layout.(map[string]any)
	var rows []any
	switch rs := l["rows"].(type) {
	case map[string]any:
		for _, row := range rs {
			rows = append(rows, row)
		}
	case []any:
		rows = rs
	}
	for _, row := range rows {
		rm, _ := row.(map[string]any)
		cells, _ := rm["cells"].([]any)
		for _, c := range cells {
			cm, _ := c.(map[string]any)
			if cm["content_type"] == contentType && fmt.Sprint(cm["content_id"]) == id {
				s, _ := cm["id"].(string)
				return s
			}
		}
	}
	return ""
}
