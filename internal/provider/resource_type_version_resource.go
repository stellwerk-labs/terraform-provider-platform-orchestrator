package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

var _ resource.Resource = &ResourceTypeVersionResource{}
var _ resource.ResourceWithImportState = &ResourceTypeVersionResource{}
var _ resource.ResourceWithModifyPlan = &ResourceTypeVersionResource{}

func NewResourceTypeVersionResource() resource.Resource { return &ResourceTypeVersionResource{} }

type ResourceTypeVersionResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ResourceTypeVersionResourceModel struct {
	ID                      types.String         `tfsdk:"id"`
	ResourceTypeID          types.String         `tfsdk:"resource_type_id"`
	Version                 types.Int64          `tfsdk:"version"`
	ExpectedResourceVersion types.Int64          `tfsdk:"expected_resource_version"`
	OutputSchema            jsontypes.Normalized `tfsdk:"output_schema"`
	ModuleContract          jsontypes.Normalized `tfsdk:"module_contract"`
	Reason                  types.String         `tfsdk:"reason"`
}

func (r *ResourceTypeVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resource_type_version"
}

func (r *ResourceTypeVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Publishes one exact immutable Resource Type interface. Version 1 belongs to the resource_type catalogue identity; new publications start at 2 and must be consecutive. Existing Module Versions keep their original contracts. Destroy forgets this state reference and never deletes an interface.", Attributes: map[string]schema.Attribute{
		"id":                        schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable interface UUID."},
		"resource_type_id":          schema.StringAttribute{Required: true, PlanModifiers: replace},
		"version":                   schema.Int64Attribute{Required: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, MarkdownDescription: "Exact next interface number, at least 2. This is permanent identity, not a catalogue concurrency counter."},
		"expected_resource_version": schema.Int64Attribute{Required: true, MarkdownDescription: "Reviewed catalogue concurrency revision used only when creating this publication. Updating this input on an existing interface does not republish it."},
		"output_schema":             schema.StringAttribute{Required: true, CustomType: jsontypes.NormalizedType{}, PlanModifiers: replace},
		"module_contract":           schema.StringAttribute{Optional: true, Computed: true, CustomType: jsontypes.NormalizedType{}, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Optional bounded Module declaration contract as JSON. Absence remains null."},
		"reason":                    schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Immutable recorded publication reason."},
	}}
}

func (r *ResourceTypeVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got %T", req.ProviderData))
		return
	}
	r.cpClient, r.orgID = data.CpClient, data.OrgId
}

func (r *ResourceTypeVersionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state ResourceTypeVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !plan.ResourceTypeID.Equal(state.ResourceTypeID) || !plan.Version.Equal(state.Version) {
		return
	}
	if resourceTypeVersionContentChanged(ctx, plan, state, true) {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Resource Type interfaces are immutable. Choose a new version number when changing their schema, contract or reason.")
	}
}

func resourceTypeVersionContentChanged(ctx context.Context, plan, state ResourceTypeVersionResourceModel, allowUnknown bool) bool {
	for _, pair := range [][2]jsontypes.Normalized{{plan.OutputSchema, state.OutputSchema}, {plan.ModuleContract, state.ModuleContract}} {
		if allowUnknown && pair[0].IsUnknown() {
			continue
		}
		if pair[0].Equal(pair[1]) {
			continue
		}
		if pair[0].IsNull() || pair[1].IsNull() || pair[0].IsUnknown() || pair[1].IsUnknown() {
			return true
		}
		equal, diagnostics := pair[0].StringSemanticEquals(ctx, pair[1])
		if diagnostics.HasError() || !equal {
			return true
		}
	}
	if allowUnknown && plan.Reason.IsUnknown() {
		return false
	}
	return strings.TrimSpace(plan.Reason.ValueString()) != strings.TrimSpace(state.Reason.ValueString())
}

func applyResourceTypeVersionState(state *ResourceTypeVersionResourceModel, value cp.ResourceTypeVersion) error {
	output, err := json.Marshal(value.OutputSchema)
	if err != nil {
		return err
	}
	state.ID = types.StringValue(value.Uuid.String())
	state.ResourceTypeID, state.Version = types.StringValue(value.ResourceTypeId), types.Int64Value(value.Version)
	state.OutputSchema = jsontypes.NewNormalizedValue(string(output))
	state.ModuleContract = jsontypes.NewNormalizedNull()
	if value.ModuleContract != nil {
		contract, err := json.Marshal(*value.ModuleContract)
		if err != nil {
			return err
		}
		state.ModuleContract = jsontypes.NewNormalizedValue(string(contract))
	}
	if value.Reason == nil {
		state.Reason = types.StringNull()
	} else if strings.TrimSpace(state.Reason.ValueString()) != *value.Reason {
		state.Reason = types.StringValue(*value.Reason)
	}
	return nil
}

func (r *ResourceTypeVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ResourceTypeVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Version.ValueInt64() < 2 || plan.ExpectedResourceVersion.ValueInt64() < 1 || strings.TrimSpace(plan.Reason.ValueString()) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "A publication requires version>=2, positive expected_resource_version and a non-empty reason.")
		return
	}
	body := cp.ResourceTypeVersionPublishBody{Version: plan.Version.ValueInt64(), ExpectedResourceVersion: plan.ExpectedResourceVersion.ValueInt64(), Reason: plan.Reason.ValueString()}
	resp.Diagnostics.Append(plan.OutputSchema.Unmarshal(&body.OutputSchema)...)
	if !plan.ModuleContract.IsNull() && !plan.ModuleContract.IsUnknown() {
		var contract cp.ResourceTypeModuleContract
		resp.Diagnostics.Append(plan.ModuleContract.Unmarshal(&contract)...)
		body.ModuleContract = &contract
	}
	if resp.Diagnostics.HasError() {
		return
	}
	key := moduleVersionCommandKey(r.orgID, plan.ResourceTypeID.ValueString(), strconv.FormatInt(body.Version, 10), "resource-type-interface")
	result, err := r.cpClient.PublishResourceTypeVersionWithResponse(ctx, r.orgID, plan.ResourceTypeID.ValueString(), &cp.PublishResourceTypeVersionParams{IdempotencyKey: key}, body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
		return
	}
	if result.StatusCode() != http.StatusCreated || result.JSON201 == nil {
		addAPIResponseError(&resp.Diagnostics, "publish exact Resource Type interface (import it if already retained)", result.StatusCode(), result.Body)
		return
	}
	if err := applyResourceTypeVersionState(&plan, *result.JSON201); err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ResourceTypeVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ResourceTypeVersionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.cpClient.GetResourceTypeVersionWithResponse(ctx, r.orgID, state.ResourceTypeID.ValueString(), state.Version.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
		return
	}
	if result.StatusCode() != http.StatusOK || result.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read retained Resource Type interface", result.StatusCode(), result.Body)
		return
	}
	if err := applyResourceTypeVersionState(&state, *result.JSON200); err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	if state.ExpectedResourceVersion.IsNull() || state.ExpectedResourceVersion.IsUnknown() {
		parent, err := r.cpClient.GetResourceTypeWithResponse(ctx, r.orgID, state.ResourceTypeID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, err.Error())
			return
		}
		if parent.StatusCode() != http.StatusOK || parent.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "read catalogue concurrency revision", parent.StatusCode(), parent.Body)
			return
		}
		state.ExpectedResourceVersion = types.Int64Value(parent.JSON200.ResourceVersion)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ResourceTypeVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ResourceTypeVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.ResourceTypeID.Equal(state.ResourceTypeID) || !plan.Version.Equal(state.Version) || resourceTypeVersionContentChanged(ctx, plan, state, false) {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Published interfaces cannot be edited; publish a new exact version.")
		return
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ResourceTypeVersionResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Resource Type interface retained", "The immutable interface and all Module bindings remain in the Orchestrator. Terraform removed only its state reference.")
	resp.State.RemoveResource(ctx)
}

func (r *ResourceTypeVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, version, found := strings.Cut(req.ID, "/")
	number, err := strconv.ParseInt(version, 10, 64)
	if !found || id == "" || err != nil || number < 2 {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import ID must be <resource-type-id>/<interface-version>=2; version 1 belongs to the catalogue resource.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_type_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("version"), number)...)
}
