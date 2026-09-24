package provider

import (
	"context"
	"fmt"
	"net/http"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &ModuleVersionPinDataSource{}

func NewModuleVersionPinDataSource() datasource.DataSource { return &ModuleVersionPinDataSource{} }

type ModuleVersionPinDataSource struct {
	cpClient cp.ClientWithResponsesInterface
	orgID    string
}

type ModuleVersionPinDataSourceModel struct {
	ID                   types.String `tfsdk:"id"`
	ProjectUUID          types.String `tfsdk:"project_uuid"`
	EnvironmentUUID      types.String `tfsdk:"environment_uuid"`
	ModuleUUID           types.String `tfsdk:"module_uuid"`
	VersionUUID          types.String `tfsdk:"version_uuid"`
	Status               types.String `tfsdk:"status"`
	ResourceVersion      types.Int64  `tfsdk:"resource_version"`
	ActivationEventID    types.String `tfsdk:"activation_event_id"`
	CreatedBy            types.String `tfsdk:"created_by"`
	CreationReason       types.String `tfsdk:"creation_reason"`
	OverrideActor        types.String `tfsdk:"override_actor"`
	OverrideReason       types.String `tfsdk:"override_reason"`
	OverrideOperationID  types.String `tfsdk:"override_operation_id"`
	OverrideDeploymentID types.String `tfsdk:"override_deployment_id"`
	EventsJSON           types.String `tfsdk:"events_json"`
}

func (d *ModuleVersionPinDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_module_version_pin"
}

func (d *ModuleVersionPinDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads one exact Orchestrator Environment Module Version Pin and its complete append-only history.",
		Attributes: map[string]schema.Attribute{
			"id":                     schema.StringAttribute{Required: true, MarkdownDescription: "Module Version Pin UUID."},
			"project_uuid":           schema.StringAttribute{Computed: true},
			"environment_uuid":       schema.StringAttribute{Computed: true},
			"module_uuid":            schema.StringAttribute{Computed: true},
			"version_uuid":           schema.StringAttribute{Computed: true},
			"status":                 schema.StringAttribute{Computed: true},
			"resource_version":       schema.Int64Attribute{Computed: true},
			"activation_event_id":    schema.StringAttribute{Computed: true, MarkdownDescription: "Latest event that established the current approval boundary. Notes never change it."},
			"created_by":             schema.StringAttribute{Computed: true},
			"creation_reason":        schema.StringAttribute{Computed: true},
			"override_actor":         schema.StringAttribute{Computed: true},
			"override_reason":        schema.StringAttribute{Computed: true},
			"override_operation_id":  schema.StringAttribute{Computed: true},
			"override_deployment_id": schema.StringAttribute{Computed: true},
			"events_json":            schema.StringAttribute{Computed: true, MarkdownDescription: "Complete ordered Pin lifecycle and note history as JSON."},
		},
	}
}

func (d *ModuleVersionPinDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got %T", req.ProviderData))
		return
	}
	d.cpClient, d.orgID = data.CpClient, data.OrgId
}

func (d *ModuleVersionPinDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state ModuleVersionPinDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pinID, err := uuid.Parse(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "id must be a Module Version Pin UUID: "+err.Error())
		return
	}
	pinResponse, err := d.cpClient.GetEnvironmentModuleVersionPinWithResponse(ctx, d.orgID, pinID)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module Version Pin: "+err.Error())
		return
	}
	if pinResponse.StatusCode() != http.StatusOK || pinResponse.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Module Version Pin", pinResponse.StatusCode(), pinResponse.Body)
		return
	}
	eventsResponse, err := d.cpClient.ListEnvironmentModuleVersionPinEventsWithResponse(ctx, d.orgID, pinID)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, "Unable to read Module Version Pin history: "+err.Error())
		return
	}
	if eventsResponse.StatusCode() != http.StatusOK || eventsResponse.JSON200 == nil {
		addAPIResponseError(&resp.Diagnostics, "read Module Version Pin history", eventsResponse.StatusCode(), eventsResponse.Body)
		return
	}
	pin := *pinResponse.JSON200
	state.ID = types.StringValue(pin.Id.String())
	state.ProjectUUID = types.StringValue(pin.ProjectUuid.String())
	state.EnvironmentUUID = types.StringValue(pin.EnvironmentUuid.String())
	state.ModuleUUID = types.StringValue(pin.ModuleUuid.String())
	state.VersionUUID = types.StringValue(pin.VersionUuid.String())
	state.Status = types.StringValue(string(pin.Status))
	state.ResourceVersion = types.Int64Value(pin.ResourceVersion)
	state.ActivationEventID = types.StringValue(pin.ActivationEventId.String())
	state.CreatedBy = types.StringValue(pin.CreatedBy.String())
	state.OverrideActor = nullableUUIDString(pin.OverrideActor)
	state.OverrideReason = nullableString(pin.OverrideReason)
	state.OverrideOperationID = nullableUUIDString(pin.OverrideOperationId)
	state.OverrideDeploymentID = nullableUUIDString(pin.OverrideDeploymentId)
	state.CreationReason = types.StringNull()
	for _, event := range *eventsResponse.JSON200 {
		if event.EventType == "created" && event.Reason != nil {
			state.CreationReason = types.StringValue(*event.Reason)
			break
		}
	}
	if !setJSONString(&resp.Diagnostics, &state.EventsJSON, eventsResponse.JSON200) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func nullableString(value *string) types.String {
	if value == nil {
		return types.StringNull()
	}
	return types.StringValue(*value)
}
