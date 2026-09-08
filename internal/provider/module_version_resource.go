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
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &ModuleVersionResource{}
var _ resource.ResourceWithImportState = &ModuleVersionResource{}

func NewModuleVersionResource() resource.Resource { return &ModuleVersionResource{} }

type ModuleVersionResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleVersionResourceModel struct {
	ID               types.String         `tfsdk:"id"`
	ModuleID         types.String         `tfsdk:"module_id"`
	SemanticVersion  types.String         `tfsdk:"semantic_version"`
	Definition       jsontypes.Normalized `tfsdk:"definition"`
	LifecycleStatus  types.String         `tfsdk:"lifecycle_status"`
	TransitionReason types.String         `tfsdk:"transition_reason"`
	ResourceVersion  types.Int64          `tfsdk:"resource_version"`
	Verification     types.String         `tfsdk:"verification_status"`
}

func (r *ModuleVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_version"
}

func (r *ModuleVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Publishes and manages one immutable Core Module Version. The external artifact definition and SemVer identity are immutable; removing this Terraform resource only forgets it from state because Stellwerk permanently retains published Versions.",
		Attributes: map[string]schema.Attribute{
			"id":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable Module Version UUID."},
			"module_id":           schema.StringAttribute{Required: true, MarkdownDescription: "Immutable Module technical slug.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"semantic_version":    schema.StringAttribute{Required: true, MarkdownDescription: "Canonical SemVer identity. New publications begin Proposed and Unverified.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"definition":          schema.StringAttribute{Required: true, CustomType: jsontypes.NormalizedType{}, MarkdownDescription: "JSON Module Version definition containing source, inputs, parameters, provider mappings, dependencies, co-provisioned resources, author-declared output_schema and release notes. Declare the SemVer identity with semantic_version, not inside this JSON payload. New publications bound to a nonempty Resource Type output_schema require the exact same output declaration. External sources require source_revision; artifact_digest is optional and immutable when supplied, and forbidden for inline source. Unknown definition fields are rejected.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"lifecycle_status":    schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Desired Core lifecycle status. Valid values are proposed, default, deprecated and defective; transitions remain server validated.", Validators: []validator.String{stringvalidator.OneOf("proposed", "default", "deprecated", "defective")}},
			"transition_reason":   schema.StringAttribute{Optional: true, MarkdownDescription: "Human-readable reason used when lifecycle_status requests a transition after publication."},
			"resource_version":    schema.Int64Attribute{Computed: true, MarkdownDescription: "Optimistic-concurrency version of the lifecycle record."},
			"verification_status": schema.StringAttribute{Computed: true, MarkdownDescription: "Artifact verification state. The first iteration reports unverified without blocking explicit use."},
		},
	}
}

func (r *ModuleVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func moduleVersionCommandKey(orgID, moduleID, version, action string) string {
	return uuid.NewSHA1(uuid.Nil, []byte(orgID+"\x00"+moduleID+"\x00"+version+"\x00"+action)).String()
}

func (r *ModuleVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ModuleVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	desiredLifecycle := plan.LifecycleStatus.ValueString()
	transitionReason := plan.TransitionReason.ValueString()
	if desiredLifecycle != "" && desiredLifecycle != string(cp.ModuleVersionSemanticStatusProposed) && strings.TrimSpace(transitionReason) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "transition_reason is required when publishing directly to a lifecycle status other than proposed")
		return
	}
	var body cp.ModuleVersionPublishBody
	if err := decodeModuleVersionDefinition(plan.Definition.ValueString(), &body); err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "definition is not a valid Module Version publication: "+err.Error())
		return
	}
	body.SemanticVersion = plan.SemanticVersion.ValueString()
	response, err := r.cpClient.PublishModuleVersionWithResponse(ctx, r.orgID, plan.ModuleID.ValueString(),
		&cp.PublishModuleVersionParams{IdempotencyKey: moduleVersionCommandKey(r.orgID, plan.ModuleID.ValueString(), body.SemanticVersion, "publish")}, body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to publish Module Version: "+err.Error())
		return
	}
	if response.StatusCode() != http.StatusCreated || response.JSON201 == nil {
		addAPIResponseError(&resp.Diagnostics, "publish Module Version", response.StatusCode(), response.Body)
		return
	}
	applyModuleVersionState(&plan, *response.JSON201)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if desiredLifecycle != "" && desiredLifecycle != string(cp.ModuleVersionSemanticStatusProposed) {
		if !r.transition(ctx, &plan, string(cp.ModuleVersionSemanticStatusProposed), desiredLifecycle, &resp.Diagnostics) {
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func decodeModuleVersionDefinition(definition string, body *cp.ModuleVersionPublishBody) error {
	if !json.Valid([]byte(definition)) || !strings.HasPrefix(strings.TrimSpace(definition), "{") {
		return fmt.Errorf("definition must contain one JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(definition))
	decoder.DisallowUnknownFields()
	return decoder.Decode(body)
}

func (r *ModuleVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ModuleVersionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	response, err := r.cpClient.GetModuleVersionWithResponse(ctx, r.orgID, state.ModuleID.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module Version: "+err.Error())
		return
	}
	if response.StatusCode() == http.StatusNotFound {
		resp.Diagnostics.AddError(PO_RESOURCE_NOT_FOUND_ERR, "An immutable Module Version disappeared from Stellwerk; catalogue integrity requires operator investigation.")
		return
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Module Version", response.StatusCode(), response.Body)
		return
	}
	if response.JSON200.Version.MigrationGeneration == "v0" {
		resp.Diagnostics.AddError("Legacy Module Version is retained history", "Import the existing Module slug as platform-orchestrator_module_catalogue_entry. Legacy v0 has no Semantic Version or artifact digest and cannot be managed as a new Module Version publication. Publish a separate complete stable Version for the first v1 release.")
		return
	}
	applyModuleVersionState(&state, response.JSON200.Version)
	if err := applyModuleVersionDefinition(&state, moduleVersionPublishBody(*response.JSON200)); err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, "Unable to encode immutable Module Version definition: "+err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func applyModuleVersionDefinition(state *ModuleVersionResourceModel, body cp.ModuleVersionPublishBody) error {
	// The API's generated response types omit explicit false values for optional
	// fields. Preserve a configured immutable definition to avoid manufacturing
	// drift during refresh. Imports have no configured definition, so reconstruct
	// one from the authoritative API representation for that case. The SemVer is
	// resource identity, represented by the top-level semantic_version attribute,
	// so it must not be duplicated into the author definition JSON on import.
	if !state.Definition.IsNull() && !state.Definition.IsUnknown() {
		return nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return err
	}
	delete(fields, "semantic_version")
	encoded, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	state.Definition = jsontypes.NewNormalizedValue(string(encoded))
	return nil
}

func (r *ModuleVersionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ModuleVersionResourceModel
	var state ModuleVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	plan.ResourceVersion = state.ResourceVersion
	plan.Verification = state.Verification
	if plan.LifecycleStatus.IsUnknown() {
		plan.LifecycleStatus = state.LifecycleStatus
	}
	if plan.LifecycleStatus.ValueString() == state.LifecycleStatus.ValueString() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	if strings.TrimSpace(plan.TransitionReason.ValueString()) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "transition_reason is required for a Module Version lifecycle transition")
		return
	}
	if !r.transition(ctx, &plan, state.LifecycleStatus.ValueString(), plan.LifecycleStatus.ValueString(), &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ModuleVersionResource) transition(ctx context.Context, state *ModuleVersionResourceModel, current, desired string, diagnostics *diag.Diagnostics) bool {
	action, err := moduleVersionLifecycleAction(current, desired)
	if err != nil {
		diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return false
	}
	response, err := r.cpClient.TransitionModuleVersionWithResponse(ctx, r.orgID, state.ModuleID.ValueString(), state.ID.ValueString(), action,
		&cp.TransitionModuleVersionParams{IdempotencyKey: reasonedCommandKey(r.orgID, state.ID.ValueString(), string(action), state.ResourceVersion.ValueInt64(), state.TransitionReason.ValueString())},
		cp.ModuleReasonedCommand{ExpectedResourceVersion: state.ResourceVersion.ValueInt64(), Reason: state.TransitionReason.ValueString()})
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, "Unable to transition Module Version: "+err.Error())
		return false
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(diagnostics, "transition Module Version", response.StatusCode(), response.Body)
		return false
	}
	applyModuleVersionState(state, *response.JSON200)
	return true
}

func moduleVersionLifecycleAction(current, desired string) (cp.TransitionModuleVersionParamsLifecycleAction, error) {
	switch {
	case current == "proposed" && desired == "default":
		return cp.TransitionModuleVersionParamsLifecycleActionPromote, nil
	case desired == "deprecated":
		return cp.TransitionModuleVersionParamsLifecycleActionDeprecate, nil
	case desired == "defective":
		return cp.TransitionModuleVersionParamsLifecycleActionMarkDefective, nil
	case current == "deprecated" && desired == "default":
		// Core permits this only for the exact immediately preceding Default. The
		// server remains authoritative and performs the atomic lineage swap.
		return cp.TransitionModuleVersionParamsLifecycleActionRestore, nil
	default:
		return "", fmt.Errorf("unsupported Module Version lifecycle transition %q -> %q", current, desired)
	}
}

func applyModuleVersionState(state *ModuleVersionResourceModel, version cp.CoreModuleVersion) {
	state.ID = types.StringValue(version.Uuid.String())
	state.ModuleID = types.StringValue(version.ModuleSlug)
	state.SemanticVersion = types.StringValue(version.OpaqueVersionId)
	state.LifecycleStatus = types.StringValue(string(version.LifecycleStatus))
	state.ResourceVersion = types.Int64Value(version.ResourceVersion)
	state.Verification = types.StringValue(string(version.VerificationStatus))
}

func moduleVersionPublishBody(detail cp.CoreModuleVersionDetail) cp.ModuleVersionPublishBody {
	return cp.ModuleVersionPublishBody{
		ArtifactDigest: func() *string {
			if detail.Version.ArtifactDigest == "" {
				return nil
			}
			value := detail.Version.ArtifactDigest
			return &value
		}(),
		Coprovisioned: detail.Coprovisioned, Dependencies: detail.Dependencies, Description: detail.Description,
		ModuleInputs: detail.ModuleInputs, ModuleParams: detail.ModuleParams, ModuleSource: detail.ModuleSource,
		ModuleSourceCode: detail.ModuleSourceCode, ProviderMapping: detail.ProviderMapping, ReleaseNotes: detail.Version.ReleaseNotes,
		OutputSchema:    detail.OutputSchema,
		SemanticVersion: detail.Version.OpaqueVersionId,
		SourceRevision: func() *string {
			if detail.Version.SourceRevision == "" {
				return nil
			}
			value := detail.Version.SourceRevision
			return &value
		}(),
	}
}

func (r *ModuleVersionResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Immutable Module Version retained", "Stellwerk permanently retains published Module Versions. Terraform removed only its state reference; no API deletion was attempted.")
	resp.State.RemoveResource(ctx)
}

func (r *ModuleVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import ID must be <module-id>/<module-version-uuid-or-semver>")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("module_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
