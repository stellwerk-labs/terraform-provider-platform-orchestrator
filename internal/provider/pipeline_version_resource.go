package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/pipelines"
)

var _ resource.Resource = &PipelineVersionResource{}
var _ resource.ResourceWithImportState = &PipelineVersionResource{}

func NewPipelineVersionResource() resource.Resource { return &PipelineVersionResource{} }

type PipelineVersionResource struct {
	client pipelines.ClientWithResponsesInterface
	orgID  string
}
type PipelineVersionResourceModel struct {
	ID                      types.String `tfsdk:"id"`
	ProjectID               types.String `tfsdk:"project_id"`
	PipelineID              types.String `tfsdk:"pipeline_id"`
	ExpectedResourceVersion types.Int64  `tfsdk:"expected_resource_version"`
	RequestKey              types.String `tfsdk:"request_key"`
	Version                 types.Int64  `tfsdk:"version"`
	Digest                  types.String `tfsdk:"digest"`
	Retired                 types.Bool   `tfsdk:"retired"`
}

func (r *PipelineVersionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline_version"
}
func (r *PipelineVersionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{MarkdownDescription: "Publishes one immutable reviewed pipeline draft revision. Does not allow or select it in project policy. Delete retires the publication and refuses while policy references it; remove references explicitly first. Use create_before_destroy when publishing a replacement, then move policy references before retirement.", Attributes: map[string]schema.Attribute{
		"id":                        schema.StringAttribute{Computed: true, MarkdownDescription: "Immutable published version UUID."},
		"project_id":                schema.StringAttribute{Required: true, PlanModifiers: replace},
		"pipeline_id":               schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Authoring draft UUID to publish."},
		"expected_resource_version": schema.Int64Attribute{Required: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, MarkdownDescription: "Exact reviewed draft revision. Server rejects stale publication."},
		"request_key":               schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Stable caller-owned publication key (1-128 characters). Reuse for retries, change for a distinct publication intent. Scoped to authenticated actor and project."},
		"version":                   schema.Int64Attribute{Computed: true, MarkdownDescription: "Published numeric version, distinct from draft resource_version."},
		"digest":                    schema.StringAttribute{Computed: true, MarkdownDescription: "Server-computed immutable recipe digest."},
		"retired":                   schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether this immutable version has been retired. Retirement does not erase its history; choose a new reviewed draft revision and request key to replace it."},
	}}
}
func (r *PipelineVersionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func setPipelineVersion(data *PipelineVersionResourceModel, version pipelines.PublishedPipelineVersion) {
	data.ID = types.StringValue(version.Id.String())
	data.PipelineID = types.StringValue(version.PipelineId.String())
	data.ProjectID = types.StringValue(version.ProjectId)
	data.Version = types.Int64Value(int64(version.Version))
	data.Digest = types.StringValue(version.Digest)
	data.Retired = types.BoolValue(version.RetiredAt != nil)
}
func pipelineLifecycleError(operation string, status int) string {
	if status == http.StatusConflict {
		return operation + " conflicted. Refresh and review the exact draft revision or policy. Remove policy references before retiring a version; no policy is changed automatically."
	}
	if status == http.StatusForbidden {
		return operation + " forbidden; check project pipeline publication permissions."
	}
	return fmt.Sprintf("%s returned HTTP %d.", operation, status)
}
func pipelinePublicationInput(data PipelineVersionResourceModel) (uuid.UUID, error) {
	id, err := uuid.Parse(data.PipelineID.ValueString())
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("pipeline_id must be a nonzero UUID")
	}
	key := data.RequestKey.ValueString()
	if data.ExpectedResourceVersion.ValueInt64() < 1 || strings.TrimSpace(key) == "" || len(key) > 128 {
		return uuid.Nil, fmt.Errorf("expected_resource_version must be positive and request_key must contain 1-128 characters")
	}
	return id, nil
}
func (r *PipelineVersionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PipelineVersionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := pipelinePublicationInput(data)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	result, err := r.client.PublishProjectPipelineWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), id, pipelines.PipelinePublish{ExpectedResourceVersion: data.ExpectedResourceVersion.ValueInt64(), RequestKey: data.RequestKey.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Publish pipeline request failed: "+err.Error())
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineLifecycleError("Publish pipeline", result.StatusCode()))
		return
	}
	// Publication replay retains its original response; read current retirement separately.
	current, err := r.client.GetPublishedProjectPipelineWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), result.JSON200.Id)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Read published pipeline status failed: "+err.Error())
		return
	}
	if current.StatusCode() != 200 || current.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineLifecycleError("Read published pipeline status", current.StatusCode()))
		return
	}
	if current.JSON200.RetiredAt != nil {
		resp.Diagnostics.AddError(PO_API_ERR, "This publication intent identifies a retired version. Review and publish a new draft revision with a new request_key; retired versions are never reactivated.")
		return
	}
	setPipelineVersion(&data, *current.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *PipelineVersionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PipelineVersionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := uuid.Parse(data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Published pipeline ID must be a UUID.")
		return
	}
	result, err := r.client.GetPublishedProjectPipelineWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), id)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Read pipeline version request failed: "+err.Error())
		return
	}
	if result.StatusCode() == 404 {
		resp.State.RemoveResource(ctx)
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineLifecycleError("Read pipeline version", result.StatusCode()))
		return
	}
	if result.JSON200.RetiredAt != nil {
		resp.Diagnostics.AddWarning("Pipeline version retired", "This immutable version was retired. Its history remains readable; review a new draft revision and publication key to replace it.")
	}
	if !data.PipelineID.IsNull() && data.PipelineID.ValueString() != result.JSON200.PipelineId.String() {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Imported pipeline_id does not match the actual immutable publication.")
		return
	}
	if data.ProjectID.ValueString() != result.JSON200.ProjectId {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Imported project_id does not match the actual immutable publication.")
		return
	}
	setPipelineVersion(&data, *result.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *PipelineVersionResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(PO_INPUT_ERR, "Published pipeline versions are immutable; input changes require replacement.")
}
func (r *PipelineVersionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PipelineVersionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := uuid.Parse(data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Published pipeline ID must be a UUID.")
		return
	}
	result, err := r.client.RetireProjectPipelineWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), id)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Retire pipeline request failed: "+err.Error())
		return
	}
	if result.StatusCode() != 200 && result.StatusCode() != 404 {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineLifecycleError("Retire pipeline", result.StatusCode()))
	}
}
func (r *PipelineVersionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	var identity struct {
		ProjectID  string `json:"project_id"`
		ID         string `json:"id"`
		PipelineID string `json:"pipeline_id"`
		Revision   int64  `json:"expected_resource_version"`
		RequestKey string `json:"request_key"`
	}
	decoder := json.NewDecoder(strings.NewReader(req.ID))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import identity must be JSON with project_id, id, pipeline_id, expected_resource_version and original request_key.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import identity must contain exactly one JSON object.")
		return
	}
	data := PipelineVersionResourceModel{ID: types.StringValue(identity.ID), ProjectID: types.StringValue(identity.ProjectID), PipelineID: types.StringValue(identity.PipelineID), ExpectedResourceVersion: types.Int64Value(identity.Revision), RequestKey: types.StringValue(identity.RequestKey)}
	if id, err := uuid.Parse(identity.ID); err != nil || id == uuid.Nil || identity.ProjectID == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import requires project_id and a published version UUID.")
		return
	}
	if _, err := pipelinePublicationInput(data); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("pipeline_id"), PO_INPUT_ERR, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
