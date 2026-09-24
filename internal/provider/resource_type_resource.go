package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &ResourceTypeResource{}
var _ resource.ResourceWithImportState = &ResourceTypeResource{}
var _ resource.ResourceWithModifyPlan = &ResourceTypeResource{}

func NewResourceTypeResource() resource.Resource {
	return &ResourceTypeResource{}
}

// ResourceTypeResource defines the resource implementation.
type ResourceTypeResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgId    string
}

// ResourceTypeResourceModel describes the resource data model.
type ResourceTypeResourceModel struct {
	Id                    types.String         `tfsdk:"id"`
	Description           types.String         `tfsdk:"description"`
	OutputSchema          jsontypes.Normalized `tfsdk:"output_schema"`
	ModuleContract        jsontypes.Normalized `tfsdk:"module_contract"`
	IsDeveloperAccessible types.Bool           `tfsdk:"is_developer_accessible"`
	Status                types.String         `tfsdk:"status"`
	TransitionReason      types.String         `tfsdk:"transition_reason"`
	ResourceVersion       types.Int64          `tfsdk:"resource_version"`
	DeletionPolicy        types.String         `tfsdk:"deletion_policy"`
}

func (r *ResourceTypeResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resource_type"
}

func (r *ResourceTypeResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// This description is used by the documentation generator and the language server.
		MarkdownDescription: "Manages an immutable Orchestrator Resource Type contract. Changing its contract requires a new identity. Archive blocks new Module bindings while preserving existing Modules. Referenced identities cannot be deleted; explicitly set deletion_policy=retain to relinquish Terraform ownership without deletion.",

		Attributes: map[string]schema.Attribute{
			"deletion_policy":   retainedDependencyPolicyAttribute(),
			"status":            schema.StringAttribute{Optional: true, Computed: true, Validators: []validator.String{stringvalidator.OneOf("active", "archived")}, MarkdownDescription: "Catalogue status: active or archived. Archival blocks only new Module bindings."},
			"transition_reason": schema.StringAttribute{Optional: true, MarkdownDescription: "Mandatory reason when status changes."},
			"resource_version":  schema.Int64Attribute{Computed: true, MarkdownDescription: "Optimistic-concurrency revision."},
			"id": schema.StringAttribute{
				MarkdownDescription: "The unique identifier for the Resource Type.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[a-z](?:-?[a-z0-9]+)+$`),
						"must start with a lowercase letter, can contain lowercase letters, numbers, and hyphens and can not be empty.",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "The description of the Resource Type.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtMost(200),
				},
			},
			"output_schema": schema.StringAttribute{
				MarkdownDescription: "The JSON schema for output parameters.",
				Required:            true,
				CustomType:          jsontypes.NormalizedType{},
			},
			"module_contract": schema.StringAttribute{
				MarkdownDescription: "Optional immutable, bounded OpenAPI 3.0 Schema Object over module_inputs, module_params, provider_mapping, dependencies, coprovisioned and output_schema. Validated offline by the Orchestrator; no external artifact inspection. Omission preserves imported contracts and imposes no additional constraints on new Resource Types.",
				Optional:            true, Computed: true, CustomType: jsontypes.NormalizedType{},
			},
			"is_developer_accessible": schema.BoolAttribute{
				MarkdownDescription: "Indicates if this resource type is for developers to use in the manifest. Resource types with this flag set to false, will not be available as types of resources in a manifest.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
		},
	}
}

func (r *ResourceTypeResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	providerData, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(
			PO_PROVIDER_ERR,
			fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.cpClient = providerData.CpClient
	r.orgId = providerData.OrgId
}

func (r *ResourceTypeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ResourceTypeResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}
	desiredStatus := data.Status.ValueString()
	if desiredStatus == "archived" && strings.TrimSpace(data.TransitionReason.ValueString()) == "" {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "transition_reason is required when creating an archived Resource Type")
		return
	}

	var description *string
	if v := data.Description.ValueString(); v != "" {
		description = &v
	}

	var outputSchema map[string]interface{}
	diags := data.OutputSchema.Unmarshal(&outputSchema)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	isDeveloperAccessible := data.IsDeveloperAccessible.ValueBool()
	var moduleContract *cp.ResourceTypeModuleContract
	if !data.ModuleContract.IsNull() && !data.ModuleContract.IsUnknown() {
		var value cp.ResourceTypeModuleContract
		resp.Diagnostics.Append(data.ModuleContract.Unmarshal(&value)...)
		if resp.Diagnostics.HasError() {
			return
		}
		moduleContract = &value
	}

	httpResp, err := r.cpClient.CreateResourceTypeWithResponse(ctx, r.orgId, cp.CreateResourceTypeJSONRequestBody{
		Id:                    data.Id.ValueString(),
		Description:           description,
		OutputSchema:          outputSchema,
		ModuleContract:        moduleContract,
		IsDeveloperAccessible: &isDeveloperAccessible,
	})
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to create resource type, got error: %s", err))
		return
	}

	if httpResp.StatusCode() != 201 {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to create resource type, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}

	if !applyResourceTypeState(&data, *httpResp.JSON201, &resp.Diagnostics) {
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if desiredStatus == "archived" {
		if !r.transition(ctx, &data, cp.ChangeResourceTypeCatalogueStatusParamsCatalogueActionArchive, &resp.Diagnostics) {
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}
}

func (r *ResourceTypeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ResourceTypeResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	httpResp, err := r.cpClient.GetResourceTypeWithResponse(ctx, r.orgId, data.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Unable to read resource type, got error: %s", err))
		return
	}

	if httpResp.StatusCode() == http.StatusNotFound {
		resp.Diagnostics.AddWarning(PO_RESOURCE_NOT_FOUND_ERR, fmt.Sprintf("Resource type with ID %s not found in org %s", data.Id.ValueString(), r.orgId))
		resp.State.RemoveResource(ctx)
		return
	}

	if httpResp.StatusCode() != 200 {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to read resource type, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}

	if !applyResourceTypeState(&data, *httpResp.JSON200, &resp.Diagnostics) {
		return
	}

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ResourceTypeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, state ResourceTypeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}

	if resourceTypeContractChanged(ctx, state, data) {
		resp.Diagnostics.AddError("Resource Type contract is immutable", "Create a new Resource Type ID and bind a new Module identity to it. Existing Resource Type descriptions, output schemas and developer-accessible contracts cannot be changed or reused.")
		return
	}
	data.ResourceVersion = state.ResourceVersion
	if data.ModuleContract.IsUnknown() {
		data.ModuleContract = state.ModuleContract
	}
	if data.Status.IsUnknown() {
		data.Status = state.Status
	}
	if !data.Status.Equal(state.Status) {
		if strings.TrimSpace(data.TransitionReason.ValueString()) == "" {
			resp.Diagnostics.AddError(PO_INPUT_ERR, "transition_reason is required when Resource Type status changes")
			return
		}
		action := cp.ChangeResourceTypeCatalogueStatusParamsCatalogueActionArchive
		if data.Status.ValueString() == "active" {
			action = cp.ChangeResourceTypeCatalogueStatusParamsCatalogueActionUnarchive
		}
		if !r.transition(ctx, &data, action, &resp.Diagnostics) {
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ResourceTypeResource) transition(ctx context.Context, state *ResourceTypeResourceModel, action cp.ChangeResourceTypeCatalogueStatusParamsCatalogueAction, diagnostics *diag.Diagnostics) bool {
	response, err := r.cpClient.ChangeResourceTypeCatalogueStatusWithResponse(ctx, r.orgId, state.Id.ValueString(), action,
		&cp.ChangeResourceTypeCatalogueStatusParams{IdempotencyKey: reasonedCommandKey(r.orgId, state.Id.ValueString(), "resource-type-"+string(action), state.ResourceVersion.ValueInt64(), state.TransitionReason.ValueString())},
		cp.ModuleReasonedCommand{ExpectedResourceVersion: state.ResourceVersion.ValueInt64(), Reason: state.TransitionReason.ValueString()})
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, "Unable to change Resource Type catalogue status: "+err.Error())
		return false
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(diagnostics, "change Resource Type catalogue status", response.StatusCode(), response.Body)
		return false
	}
	return applyResourceTypeState(state, *response.JSON200, diagnostics)
}

func resourceTypeContractChanged(ctx context.Context, state, plan ResourceTypeResourceModel) bool {
	return (!plan.Description.IsUnknown() && !state.Description.Equal(plan.Description)) ||
		resourceTypeJSONChanged(ctx, state.OutputSchema, plan.OutputSchema) ||
		resourceTypeJSONChanged(ctx, state.ModuleContract, plan.ModuleContract) ||
		(!plan.IsDeveloperAccessible.IsUnknown() && !state.IsDeveloperAccessible.Equal(plan.IsDeveloperAccessible))
}

func resourceTypeJSONChanged(ctx context.Context, state, plan jsontypes.Normalized) bool {
	if plan.IsUnknown() || state.Equal(plan) {
		return false
	}
	if state.IsNull() || plan.IsNull() {
		return true
	}
	equal, diagnostics := state.StringSemanticEquals(ctx, plan)
	return diagnostics.HasError() || !equal
}

func (r *ResourceTypeResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan ResourceTypeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !state.Id.Equal(plan.Id) {
		return
	}
	if resourceTypeContractChanged(ctx, state, plan) {
		resp.Diagnostics.AddError("Resource Type contract is immutable", "Create a new Resource Type ID and bind a new Module identity to it. Replacing a contract under the same ID is not supported, including when archived.")
	}
}

func (r *ResourceTypeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ResourceTypeResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}
	if data.DeletionPolicy.ValueString() == "retain" {
		resp.Diagnostics.AddWarning("Resource Type retained", "deletion_policy=retain removed only Terraform ownership. The immutable Resource Type and its Module references remain in Stellwerk; no deletion or archival was attempted.")
		resp.State.RemoveResource(ctx)
		return
	}

	httpResp, err := r.cpClient.DeleteResourceTypeWithResponse(ctx, r.orgId, data.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to delete resource type, got error: %s", err))
		return
	}

	switch httpResp.StatusCode() {
	case 204:
		// Successfully deleted, no further action needed.
	case 404:
		// If the resource is not found, we can consider it deleted.
		resp.Diagnostics.AddWarning(PO_RESOURCE_NOT_FOUND_ERR, fmt.Sprintf("Resource Type with ID %s not found, assuming it has been deleted.", data.Id.ValueString()))
	default:
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to delete resource type, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}

	resp.State.RemoveResource(ctx)
}

func (r *ResourceTypeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func toResourceTypeModel(item cp.ResourceType) (ResourceTypeResourceModel, error) {
	var description types.String
	if item.Description != nil {
		description = types.StringValue(*item.Description)
	} else {
		description = types.StringNull()
	}

	outputSchemaBytes, err := json.Marshal(item.OutputSchema)
	if err != nil {
		return ResourceTypeResourceModel{}, fmt.Errorf("unable to marshal output schema: %w", err)
	}
	moduleContract, err := resourceTypeModuleContractValue(item.ModuleContract)
	if err != nil {
		return ResourceTypeResourceModel{}, err
	}

	return ResourceTypeResourceModel{
		Id:                    types.StringValue(item.Id),
		Description:           description,
		OutputSchema:          jsontypes.NewNormalizedValue(string(outputSchemaBytes)),
		ModuleContract:        moduleContract,
		IsDeveloperAccessible: types.BoolValue(item.IsDeveloperAccessible),
		Status:                types.StringValue(string(item.CatalogueStatus)),
		ResourceVersion:       types.Int64Value(item.ResourceVersion),
	}, nil
}

func resourceTypeModuleContractValue(contract *cp.ResourceTypeModuleContract) (jsontypes.Normalized, error) {
	if contract == nil {
		return jsontypes.NewNormalizedNull(), nil
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		return jsontypes.NewNormalizedNull(), fmt.Errorf("unable to marshal module contract: %w", err)
	}
	return jsontypes.NewNormalizedValue(string(encoded)), nil
}

func applyResourceTypeState(state *ResourceTypeResourceModel, item cp.ResourceType, diagnostics *diag.Diagnostics) bool {
	result, err := toResourceTypeModel(item)
	if err != nil {
		diagnostics.AddError(PO_PROVIDER_ERR, "Unable to convert Resource Type response: "+err.Error())
		return false
	}
	result.DeletionPolicy = state.DeletionPolicy
	if result.DeletionPolicy.IsNull() {
		result.DeletionPolicy = types.StringValue("delete")
	}
	result.TransitionReason = state.TransitionReason
	*state = result
	return true
}
