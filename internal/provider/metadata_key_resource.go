package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"

	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
)

var _ resource.Resource = &MetadataKeyResource{}
var _ resource.ResourceWithImportState = &MetadataKeyResource{}

func NewMetadataKeyResource() resource.Resource { return &MetadataKeyResource{} }

type MetadataKeyResource struct {
	dpClient dp.ClientWithResponsesInterface
	orgId    string
}

type MetadataKeyModel struct {
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	SchemaType  types.String `tfsdk:"schema_type"`
	Format      types.String `tfsdk:"format"`
	Pattern     types.String `tfsdk:"pattern"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (r *MetadataKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_metadata_key"
}

func (r *MetadataKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "An organization metadata key and its validation schema.", Attributes: map[string]schema.Attribute{
		"name":        schema.StringAttribute{MarkdownDescription: "The metadata key name.", Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"description": schema.StringAttribute{MarkdownDescription: "A human-readable description.", Optional: true},
		"schema_type": schema.StringAttribute{MarkdownDescription: "The metadata value type. Currently only `string` is supported.", Required: true, Validators: []validator.String{stringvalidator.OneOf(string(dp.MetadataKeySchemaTypeString))}},
		"format":      schema.StringAttribute{MarkdownDescription: "An optional string format constraint.", Optional: true},
		"pattern":     schema.StringAttribute{MarkdownDescription: "An optional regular-expression constraint.", Optional: true},
		"created_at":  schema.StringAttribute{MarkdownDescription: "The time the metadata key was created.", Computed: true},
	}}
}

func (r *MetadataKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	providerData, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	r.dpClient = providerData.DpClient
	r.orgId = providerData.OrgId
}

func metadataKeyCreateBody(data MetadataKeyModel) dp.MetadataKeyCreateBody {
	return dp.MetadataKeyCreateBody{
		Name: data.Name.ValueString(), Description: fromStringValueToStringPointer(data.Description),
		Schema: dp.MetadataKeySchema{Type: dp.MetadataKeySchemaType(data.SchemaType.ValueString()), Format: fromStringValueToStringPointer(data.Format), Pattern: fromStringValueToStringPointer(data.Pattern)},
	}
}

func metadataKeyUpdateBody(data MetadataKeyModel) dp.MetadataKeyUpdateBody {
	typeValue := dp.UpdateMetadataKeySchemaType(data.SchemaType.ValueString())
	return dp.MetadataKeyUpdateBody{
		Description: nullableStringUpdate(data.Description),
		Schema:      &dp.UpdateMetadataKeySchema{Type: &typeValue, Format: nullableStringUpdate(data.Format), Pattern: nullableStringUpdate(data.Pattern)},
	}
}

func nullableStringUpdate(value types.String) nullable.Nullable[string] {
	if value.IsUnknown() {
		return nil
	}
	if value.IsNull() {
		return nullable.NewNullNullable[string]()
	}
	return nullable.NewNullableWithValue(value.ValueString())
}

func (r *MetadataKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data MetadataKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := r.dpClient.CreateMetadataKeyWithResponse(ctx, r.orgId, metadataKeyCreateBody(data))
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to create metadata key: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusCreated {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to create metadata key, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toMetadataKeyModel(*httpResp.JSON201))...)
}

func (r *MetadataKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data MetadataKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := r.dpClient.GetMetadataKeyWithResponse(ctx, r.orgId, data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to read metadata key: %s", err))
		return
	}
	if httpResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to read metadata key, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toMetadataKeyModel(*httpResp.JSON200))...)
}

func (r *MetadataKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data MetadataKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := r.dpClient.UpdateMetadataKeyWithResponse(ctx, r.orgId, data.Name.ValueString(), metadataKeyUpdateBody(data))
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to update metadata key: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to update metadata key, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toMetadataKeyModel(*httpResp.JSON200))...)
}

func (r *MetadataKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data MetadataKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := r.dpClient.DeleteMetadataKeyWithResponse(ctx, r.orgId, data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to delete metadata key: %s", err))
		return
	}
	if httpResp.StatusCode() != http.StatusNoContent && httpResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to delete metadata key, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
	}
}

func (r *MetadataKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}

func toMetadataKeyModel(key dp.MetadataKey) MetadataKeyModel {
	return MetadataKeyModel{
		Name: types.StringValue(key.Name), Description: toStringValueOrNil(key.Description), SchemaType: types.StringValue(string(key.Schema.Type)),
		Format: toStringValueOrNil(key.Schema.Format), Pattern: toStringValueOrNil(key.Schema.Pattern), CreatedAt: types.StringValue(key.CreatedAt.Format(time.RFC3339)),
	}
}
