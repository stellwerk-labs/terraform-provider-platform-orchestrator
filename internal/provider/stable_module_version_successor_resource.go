package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &StableModuleVersionSuccessorResource{}

func NewStableModuleVersionSuccessorResource() resource.Resource {
	return &StableModuleVersionSuccessorResource{}
}

type StableModuleVersionSuccessorResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type StableModuleVersionSuccessorResourceModel struct {
	ID                                types.String         `tfsdk:"id"`
	ModuleID                          types.String         `tfsdk:"module_id"`
	PrereleaseVersionID               types.String         `tfsdk:"prerelease_version_id"`
	ExpectedPrereleaseResourceVersion types.Int64          `tfsdk:"expected_prerelease_resource_version"`
	Reason                            types.String         `tfsdk:"reason"`
	Definition                        jsontypes.Normalized `tfsdk:"definition"`
	StableVersionUUID                 types.String         `tfsdk:"stable_version_uuid"`
	PrereleaseVersionUUID             types.String         `tfsdk:"prerelease_version_uuid"`
}

func (r *StableModuleVersionSuccessorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stable_module_version_successor"
}

func (r *StableModuleVersionSuccessorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Atomically publishes one complete stable Proposed Module Version and deprecates its exact Proposed prerelease. Terraform destroy forgets this immutable operation receipt and never reverses lifecycle history.",
		Attributes: map[string]schema.Attribute{
			"id":                                   schema.StringAttribute{Computed: true, MarkdownDescription: "Atomic lifecycle correlation UUID."},
			"module_id":                            schema.StringAttribute{Required: true, PlanModifiers: replace},
			"prerelease_version_id":                schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Exact Proposed prerelease UUID or canonical SemVer."},
			"expected_prerelease_resource_version": schema.Int64Attribute{Required: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, MarkdownDescription: "Optimistic-concurrency version of the prerelease."},
			"reason":                               schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Reason recorded while deprecating the prerelease."},
			"definition":                           schema.StringAttribute{Required: true, CustomType: jsontypes.NormalizedType{}, PlanModifiers: replace, MarkdownDescription: "Complete JSON ModuleVersionPublishBody for the separate stable Version."},
			"stable_version_uuid":                  schema.StringAttribute{Computed: true},
			"prerelease_version_uuid":              schema.StringAttribute{Computed: true},
		},
	}
}

func (r *StableModuleVersionSuccessorResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func stableSuccessorBody(state StableModuleVersionSuccessorResourceModel) (cp.StableModuleVersionSuccessorBody, error) {
	if strings.TrimSpace(state.Reason.ValueString()) == "" {
		return cp.StableModuleVersionSuccessorBody{}, fmt.Errorf("reason must not be empty")
	}
	var version cp.ModuleVersionPublishBody
	if err := json.Unmarshal([]byte(state.Definition.ValueString()), &version); err != nil {
		return cp.StableModuleVersionSuccessorBody{}, fmt.Errorf("definition is not a valid Module Version publication: %w", err)
	}
	if strings.TrimSpace(version.SemanticVersion) == "" {
		return cp.StableModuleVersionSuccessorBody{}, fmt.Errorf("definition.semantic_version must not be empty")
	}
	return cp.StableModuleVersionSuccessorBody{
		ExpectedPrereleaseResourceVersion: state.ExpectedPrereleaseResourceVersion.ValueInt64(),
		Reason:                            state.Reason.ValueString(), Version: version,
	}, nil
}

func (r *StableModuleVersionSuccessorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan StableModuleVersionSuccessorResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := stableSuccessorBody(plan)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	key := uuid.NewSHA1(uuid.Nil, []byte(r.orgID+"\x00stable-successor\x00"+plan.ModuleID.ValueString()+"\x00"+plan.PrereleaseVersionID.ValueString()+"\x00"+plan.Definition.ValueString()+"\x00"+plan.Reason.ValueString())).String()
	response, err := r.cpClient.PublishStableModuleVersionSuccessorWithResponse(ctx, r.orgID,
		plan.ModuleID.ValueString(), plan.PrereleaseVersionID.ValueString(),
		&cp.PublishStableModuleVersionSuccessorParams{IdempotencyKey: key}, body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to publish stable Module Version successor: "+err.Error())
		return
	}
	if response.StatusCode() != http.StatusCreated || response.JSON201 == nil {
		addAPIResponseError(&resp.Diagnostics, "publish stable Module Version successor", response.StatusCode(), response.Body)
		return
	}
	plan.ID = types.StringValue(response.JSON201.CorrelationId.String())
	plan.StableVersionUUID = types.StringValue(response.JSON201.Stable.Uuid.String())
	plan.PrereleaseVersionUUID = types.StringValue(response.JSON201.Prerelease.Uuid.String())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *StableModuleVersionSuccessorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state StableModuleVersionSuccessorResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for label, versionID := range map[string]string{
		"stable": state.StableVersionUUID.ValueString(), "prerelease": state.PrereleaseVersionUUID.ValueString(),
	} {
		response, err := r.cpClient.GetModuleVersionWithResponse(ctx, r.orgID, state.ModuleID.ValueString(), versionID)
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read "+label+" Module Version: "+err.Error())
			return
		}
		if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
			addAPIResponseError(&resp.Diagnostics, "read "+label+" Module Version", response.StatusCode(), response.Body)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *StableModuleVersionSuccessorResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *StableModuleVersionSuccessorResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Stable graduation retained", "Stellwerk permanently retained both immutable Module Versions and their correlated lifecycle events. Terraform removed only the operation receipt.")
	resp.State.RemoveResource(ctx)
}
