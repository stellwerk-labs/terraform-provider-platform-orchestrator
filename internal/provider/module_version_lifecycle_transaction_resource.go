package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &ModuleVersionLifecycleTransactionResource{}

func NewModuleVersionLifecycleTransactionResource() resource.Resource {
	return &ModuleVersionLifecycleTransactionResource{}
}

type ModuleVersionLifecycleTransactionResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleVersionLifecycleTransactionResourceModel struct {
	ID           types.String         `tfsdk:"id"`
	Transaction  jsontypes.Normalized `tfsdk:"transaction"`
	VersionsJSON types.String         `tfsdk:"versions_json"`
}

func (r *ModuleVersionLifecycleTransactionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_version_lifecycle_transaction"
}

func (r *ModuleVersionLifecycleTransactionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Applies a non-empty set of Orchestrator Module Version lifecycle transitions atomically. No partial transition survives validation, authorisation, concurrency or persistence failure. The resource is an immutable operation receipt.",
		Attributes: map[string]schema.Attribute{
			"id":            schema.StringAttribute{Computed: true, MarkdownDescription: "Atomic lifecycle correlation UUID."},
			"transaction":   schema.StringAttribute{Required: true, CustomType: jsontypes.NormalizedType{}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "JSON ModuleVersionLifecycleTransactionBody with exact Version IDs, expected resource versions, actions and reasons."},
			"versions_json": schema.StringAttribute{Computed: true, MarkdownDescription: "Every resulting immutable lifecycle record returned by the Orchestrator."},
		},
	}
}

func (r *ModuleVersionLifecycleTransactionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func lifecycleTransactionBody(value string) (cp.ModuleVersionLifecycleTransactionBody, error) {
	var body cp.ModuleVersionLifecycleTransactionBody
	if err := json.Unmarshal([]byte(value), &body); err != nil {
		return body, fmt.Errorf("transaction is not valid JSON: %w", err)
	}
	if len(body.Transitions) == 0 {
		return body, fmt.Errorf("transaction.transitions must not be empty")
	}
	return body, nil
}

func (r *ModuleVersionLifecycleTransactionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ModuleVersionLifecycleTransactionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := lifecycleTransactionBody(plan.Transaction.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	key := uuid.NewSHA1(uuid.Nil, []byte(r.orgID+"\x00module-lifecycle-transaction\x00"+plan.Transaction.ValueString())).String()
	response, err := r.cpClient.TransactModuleVersionLifecyclesWithResponse(ctx, r.orgID,
		&cp.TransactModuleVersionLifecyclesParams{IdempotencyKey: key}, body)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to apply Module Version lifecycle transaction: "+err.Error())
		return
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "apply Module Version lifecycle transaction", response.StatusCode(), response.Body)
		return
	}
	plan.ID = types.StringValue(response.JSON200.CorrelationId.String())
	if !setJSONString(&resp.Diagnostics, &plan.VersionsJSON, response.JSON200.Versions) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ModuleVersionLifecycleTransactionResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse) {
}

func (r *ModuleVersionLifecycleTransactionResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

func (r *ModuleVersionLifecycleTransactionResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Lifecycle transaction retained", "Stellwerk permanently retained the resulting lifecycle records and append-only events. Terraform removed only the operation receipt.")
	resp.State.RemoveResource(ctx)
}
