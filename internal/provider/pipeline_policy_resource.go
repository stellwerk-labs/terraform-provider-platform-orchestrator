package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/pipelines"
)

var _ resource.Resource = &PipelinePolicyResource{}
var _ resource.ResourceWithImportState = &PipelinePolicyResource{}

func NewPipelinePolicyResource() resource.Resource { return &PipelinePolicyResource{} }

type PipelinePolicyResource struct {
	client pipelines.ClientWithResponsesInterface
	orgID  string
}
type PipelinePolicyResourceModel struct {
	ID              types.String         `tfsdk:"id"`
	ProjectID       types.String         `tfsdk:"project_id"`
	Document        jsontypes.Normalized `tfsdk:"document"`
	ResourceVersion types.Int64          `tfsdk:"resource_version"`
}

// This resource owns both directions; requiring rollback_defaults makes clearing explicit.
type pipelinePolicyConfiguration struct {
	AllowedVersionIDs            []uuid.UUID                         `json:"allowed_version_ids"`
	DefaultVersionID             *uuid.UUID                          `json:"default_version_id,omitempty"`
	Enabled                      bool                                `json:"enabled"`
	EnvironmentDefaultVersionIDs map[string]uuid.UUID                `json:"environment_default_version_ids,omitempty"`
	RollbackDefaults             *pipelines.PipelineRollbackDefaults `json:"rollback_defaults"`
}

func (r *PipelinePolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline_policy"
}
func (r *PipelinePolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manages one project's forward and rollback pipeline-use policy with optimistic concurrency. Import an existing policy using its project ID. Delete never disables protection: first explicitly update to enabled=false, an empty allow-list and empty forward/rollback defaults, then remove the resource in a separate apply.", Attributes: map[string]schema.Attribute{
		"id":               schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"project_id":       schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"document":         schema.StringAttribute{Required: true, CustomType: jsontypes.NormalizedType{}, MarkdownDescription: "JSON policy configuration with required enabled, allowed_version_ids, and rollback_defaults ({} explicitly clears). Optional default_version_id and environment_default_version_ids. Revision is managed separately; use jsonencode(yamldecode(file(...))) for YAML. This resource owns both directions."},
		"resource_version": schema.Int64Attribute{Computed: true, MarkdownDescription: "Server policy revision used for CAS. Concurrent edits fail rather than being overwritten."},
	}}
}
func (r *PipelinePolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected provider configuration, got %T", req.ProviderData))
		return
	}
	r.client, r.orgID = data.PipelineClient, data.OrgId
}
func pipelinePolicyConfigurationFromDocument(document string) (pipelinePolicyConfiguration, error) {
	var value pipelinePolicyConfiguration
	if len(document) > 256*1024 {
		return value, fmt.Errorf("policy document exceeds 256 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("invalid pipeline policy: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return value, fmt.Errorf("policy must contain exactly one JSON document")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(document), &fields); err != nil {
		return value, err
	}
	if _, ok := fields["enabled"]; !ok || string(fields["enabled"]) == "null" || value.AllowedVersionIDs == nil || value.RollbackDefaults == nil {
		return value, fmt.Errorf("policy requires enabled, allowed_version_ids (array), and rollback_defaults (object, use {} to clear)")
	}
	return value, nil
}
func pipelinePolicyBody(document string, revision int64) io.Reader {
	// Preserve the original object so Core can reject duplicate keys and enforce its document limits.
	return strings.NewReader(fmt.Sprintf("{\"expected_resource_version\":%d,%s", revision, strings.TrimSpace(document)[1:]))
}
func pipelinePolicyState(project string, value pipelines.PipelinePolicy) (PipelinePolicyResourceModel, error) {
	rollback := value.RollbackDefaults
	if rollback == nil {
		rollback = &pipelines.PipelineRollbackDefaults{}
	}
	allowed := value.AllowedVersionIds
	if allowed == nil {
		allowed = []uuid.UUID{}
	}
	document, err := json.Marshal(pipelinePolicyConfiguration{Enabled: value.Enabled, AllowedVersionIDs: allowed, DefaultVersionID: value.DefaultVersionId, EnvironmentDefaultVersionIDs: value.EnvironmentDefaultVersionIds, RollbackDefaults: rollback})
	return PipelinePolicyResourceModel{ID: types.StringValue(project), ProjectID: types.StringValue(project), Document: jsontypes.NewNormalizedValue(string(document)), ResourceVersion: types.Int64Value(value.ResourceVersion)}, err
}
func (r *PipelinePolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PipelinePolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := pipelinePolicyConfigurationFromDocument(data.Document.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	current, err := r.client.GetProjectPipelinePolicyWithResponse(ctx, r.orgID, data.ProjectID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Read pipeline policy failed: "+err.Error())
		return
	}
	if current.StatusCode() != 200 || current.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineLifecycleError("Read pipeline policy", current.StatusCode()))
		return
	}
	if current.JSON200.ResourceVersion != 0 {
		resp.Diagnostics.AddError(PO_API_ERR, "Project pipeline policy already exists. Import it explicitly using the project ID before changing it; a failed or lost create response must not overwrite policy.")
		return
	}
	result, err := r.client.SetProjectPipelinePolicyWithBodyWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), "application/json", pipelinePolicyBody(data.Document.ValueString(), 0))
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Create pipeline policy failed: "+err.Error())
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineLifecycleError("Create pipeline policy", result.StatusCode()))
		return
	}
	saved, err := pipelinePolicyState(data.ProjectID.ValueString(), *result.JSON200)
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	saved.Document = data.Document
	resp.Diagnostics.Append(resp.State.Set(ctx, &saved)...)
}
func (r *PipelinePolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PipelinePolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.GetProjectPipelinePolicyWithResponse(ctx, r.orgID, data.ProjectID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Read pipeline policy failed: "+err.Error())
		return
	}
	if result.StatusCode() == 404 {
		resp.State.RemoveResource(ctx)
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineLifecycleError("Read pipeline policy", result.StatusCode()))
		return
	}
	saved, err := pipelinePolicyState(data.ProjectID.ValueString(), *result.JSON200)
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	// Preserve equivalent configured optional empty maps instead of introducing perpetual diffs.
	if configured, err := pipelinePolicyConfigurationFromDocument(data.Document.ValueString()); err == nil {
		canonical, marshalErr := json.Marshal(configured)
		if marshalErr == nil && string(canonical) == saved.Document.ValueString() {
			saved.Document = data.Document
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &saved)...)
}
func (r *PipelinePolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, previous PipelinePolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := pipelinePolicyConfigurationFromDocument(data.Document.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	result, err := r.client.SetProjectPipelinePolicyWithBodyWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), "application/json", pipelinePolicyBody(data.Document.ValueString(), previous.ResourceVersion.ValueInt64()))
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Update pipeline policy failed: "+err.Error())
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineLifecycleError("Update pipeline policy", result.StatusCode()))
		return
	}
	saved, err := pipelinePolicyState(data.ProjectID.ValueString(), *result.JSON200)
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	saved.Document = data.Document
	resp.Diagnostics.Append(resp.State.Set(ctx, &saved)...)
}
func pipelinePolicyCanForget(policy pipelines.PipelinePolicy, revision int64) bool {
	if policy.ResourceVersion != revision || policy.Enabled || len(policy.AllowedVersionIds) > 0 || policy.DefaultVersionId != nil || len(policy.EnvironmentDefaultVersionIds) > 0 {
		return false
	}
	return policy.RollbackDefaults == nil || (policy.RollbackDefaults.DefaultVersionId == nil && len(policy.RollbackDefaults.EnvironmentDefaultVersionIds) == 0)
}
func (r *PipelinePolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PipelinePolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.GetProjectPipelinePolicyWithResponse(ctx, r.orgID, data.ProjectID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Read pipeline policy before removal failed: "+err.Error())
		return
	}
	if result.StatusCode() == 404 {
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineLifecycleError("Read pipeline policy", result.StatusCode()))
		return
	}
	if !pipelinePolicyCanForget(*result.JSON200, data.ResourceVersion.ValueInt64()) {
		resp.Diagnostics.AddError(PO_API_ERR, "Pipeline policy is protected, still has references, or changed concurrently. Refresh, explicitly apply enabled=false with empty allowed_version_ids, no forward defaults and rollback_defaults={}, then remove this resource in a separate apply. Delete never changes server protection.")
	}
}
func (r *PipelinePolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) == "" || strings.Contains(req.ID, "/") {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import ID must be the owning project ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
