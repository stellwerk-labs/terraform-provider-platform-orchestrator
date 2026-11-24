package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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

var _ resource.Resource = &PipelineResource{}
var _ resource.ResourceWithImportState = &PipelineResource{}
var _ resource.ResourceWithModifyPlan = &PipelineResource{}

func NewPipelineResource() resource.Resource { return &PipelineResource{} }

// PipelineResource manages a draft only. Publishing and deployment policy remain explicit operations.
type PipelineResource struct {
	client pipelines.ClientWithResponsesInterface
	orgID  string
}
type PipelineResourceModel struct {
	ID              types.String         `tfsdk:"id"`
	ProjectID       types.String         `tfsdk:"project_id"`
	Document        jsontypes.Normalized `tfsdk:"document"`
	ResourceVersion types.Int64          `tfsdk:"resource_version"`
}

func (r *PipelineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline"
}
func (r *PipelineResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Declarative project pipeline draft. Does not publish versions or change deployment policy. Delete archives an unused draft; retire published versions and remove policy references first.", Attributes: map[string]schema.Attribute{
		"id":               schema.StringAttribute{MarkdownDescription: "Pipeline UUID. Import using project-id/pipeline-uuid.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"project_id":       schema.StringAttribute{MarkdownDescription: "Owning project. Changing it replaces this resource.", Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"document":         schema.StringAttribute{Required: true, CustomType: jsontypes.NormalizedType{}, MarkdownDescription: "Canonical JSON document containing slug, name and definition. Use file() for JSON or jsonencode(yamldecode(file(...))) for YAML. Slug changes replace the draft. Secrets do not belong in this document."},
		"resource_version": schema.Int64Attribute{Computed: true, MarkdownDescription: "Draft revision used for optimistic concurrency. Concurrent edits fail rather than overwrite."},
	}}
}
func (r *PipelineResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func pipelineSlug(document string) (string, error) {
	var value struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal([]byte(document), &value); err != nil {
		return "", err
	}
	if value.Slug == "" {
		return "", fmt.Errorf("pipeline document must contain a slug")
	}
	return value.Slug, nil
}
func (r *PipelineResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var planned, previous PipelineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &planned)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() || planned.Document.IsUnknown() || previous.Document.IsUnknown() {
		return
	}
	nextSlug, err := pipelineSlug(planned.Document.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("document"), PO_INPUT_ERR, err.Error())
		return
	}
	oldSlug, err := pipelineSlug(previous.Document.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, "Stored pipeline document has no valid slug; refresh or import the draft again.")
		return
	}
	if nextSlug != oldSlug {
		resp.RequiresReplace.Append(path.Root("document"))
	}
}

func pipelineAPIError(operation string, status int) string {
	switch status {
	case http.StatusConflict:
		return operation + " conflicted. Refresh the draft before retrying. Deletion requires all published versions retired and no policy references. A create conflict requires explicit import of the existing draft; it is never adopted automatically."
	case http.StatusForbidden:
		return operation + " forbidden; check project pipeline permissions."
	default:
		return fmt.Sprintf("%s returned HTTP %d.", operation, status)
	}
}
func pipelineModel(draft pipelines.PipelineDraft) (PipelineResourceModel, error) {
	raw, err := json.Marshal(pipelines.PipelineDraftCreate{Slug: draft.Slug, Name: draft.Name, Definition: draft.Definition})
	if err != nil {
		return PipelineResourceModel{}, err
	}
	return PipelineResourceModel{ID: types.StringValue(draft.Id.String()), ProjectID: types.StringValue(draft.ProjectId), Document: jsontypes.NewNormalizedValue(string(raw)), ResourceVersion: types.Int64Value(draft.ResourceVersion)}, nil
}
func (r *PipelineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PipelineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.client.CreateProjectPipelineWithBodyWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), "application/json", strings.NewReader(data.Document.ValueString()))
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Create pipeline request failed: %s", err))
		return
	}
	if result.StatusCode() != 201 || result.JSON201 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineAPIError("Create pipeline", result.StatusCode()))
		return
	}
	saved, err := pipelineModel(*result.JSON201)
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	// Preserve the configured JSON representation, whose semantics were validated by the server.
	saved.Document = data.Document
	resp.Diagnostics.Append(resp.State.Set(ctx, &saved)...)
}
func (r *PipelineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PipelineResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := uuid.Parse(data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Pipeline ID must be a UUID.")
		return
	}
	result, err := r.client.GetProjectPipelineDraftWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), id)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Read pipeline request failed: %s", err))
		return
	}
	if result.StatusCode() == 404 {
		resp.State.RemoveResource(ctx)
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineAPIError("Read pipeline", result.StatusCode()))
		return
	}
	saved, err := pipelineModel(*result.JSON200)
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &saved)...)
}
func (r *PipelineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, previous PipelineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := uuid.Parse(previous.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Pipeline ID must be a UUID.")
		return
	}
	if previous.ResourceVersion.ValueInt64() < 1 {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Refresh the pipeline to obtain its current revision.")
		return
	}
	result, err := r.client.SaveProjectPipelineDocumentWithBodyWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), id, &pipelines.SaveProjectPipelineDocumentParams{IfMatch: strconv.Quote(strconv.FormatInt(previous.ResourceVersion.ValueInt64(), 10))}, "application/json", strings.NewReader(data.Document.ValueString()))
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Update pipeline request failed: %s", err))
		return
	}
	if result.StatusCode() != 200 || result.JSON200 == nil {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineAPIError("Update pipeline", result.StatusCode()))
		return
	}
	saved, err := pipelineModel(*result.JSON200)
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	saved.Document = data.Document
	resp.Diagnostics.Append(resp.State.Set(ctx, &saved)...)
}
func (r *PipelineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PipelineResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := uuid.Parse(data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Pipeline ID must be a UUID.")
		return
	}
	if data.ResourceVersion.ValueInt64() < 1 {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Refresh the pipeline to obtain its current revision.")
		return
	}
	result, err := r.client.DeleteProjectPipelineDraftWithResponse(ctx, r.orgID, data.ProjectID.ValueString(), id, &pipelines.DeleteProjectPipelineDraftParams{IfMatch: strconv.Quote(strconv.FormatInt(data.ResourceVersion.ValueInt64(), 10))})
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Delete pipeline request failed: %s", err))
		return
	}
	if result.StatusCode() != 204 && result.StatusCode() != 404 {
		resp.Diagnostics.AddError(PO_API_ERR, pipelineAPIError("Delete pipeline", result.StatusCode()))
	}
}
func (r *PipelineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, id, ok := strings.Cut(req.ID, "/")
	if !ok || project == "" || strings.Contains(id, "/") {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import ID must be project-id/pipeline-uuid.")
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import pipeline ID must be a UUID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), project)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
