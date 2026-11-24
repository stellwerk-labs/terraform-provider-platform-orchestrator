package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

type nativeAgentRunnerModel struct {
	ID            types.String `tfsdk:"id"`
	UUID          types.String `tfsdk:"uuid"`
	Description   types.String `tfsdk:"description"`
	Configuration types.Object `tfsdk:"runner_configuration"`
	Backend       types.Object `tfsdk:"state_storage_configuration"`
}

type nativeAgentRunnerConfiguration struct {
	Key          types.String         `tfsdk:"key"`
	Capacity     types.Int64          `tfsdk:"max_parallel_jobs"`
	Protocols    types.Set            `tfsdk:"supported_protocols"`
	Capabilities types.Set            `tfsdk:"capabilities"`
	Inventory    jsontypes.Normalized `tfsdk:"native_execution"`
}

func nativeAgentConfigurationTypes() map[string]attr.Type {
	return map[string]attr.Type{"key": types.StringType, "max_parallel_jobs": types.Int64Type,
		"supported_protocols": types.SetType{ElemType: types.StringType}, "capabilities": types.SetType{ElemType: types.StringType}, "native_execution": jsontypes.NormalizedType{}}
}

func nativeAgentTrackedUUID(value types.String) bool {
	if value.IsNull() || value.IsUnknown() {
		return false
	}
	id, err := uuid.Parse(value.ValueString())
	return err == nil && id != uuid.Nil && id.String() == value.ValueString()
}

func nativeAgentIdentity(item *cp.Runner, org, id string, previous types.String) error {
	if item == nil || item.OrgId != org || item.Id != id || item.Uuid == nil || *item.Uuid == uuid.Nil {
		return fmt.Errorf("runner response does not identify the requested organization and registration; inspect it before retrying")
	}
	kind, err := item.RunnerConfiguration.Discriminator()
	if err != nil || kind != string(cp.RunnerTypeNativeAgent) {
		return fmt.Errorf("runner response is not a native-agent registration; existing state is retained")
	}
	if !previous.IsNull() && !previous.IsUnknown() && previous.ValueString() != item.Uuid.String() {
		return fmt.Errorf("runner registration UUID changed; existing state is retained, inspect the registration before retrying")
	}
	return nil
}

func nativeAgentFromAPI(ctx context.Context, item *cp.Runner, org, id string, previous types.String) (nativeAgentRunnerModel, diag.Diagnostics) {
	var model nativeAgentRunnerModel
	var diags diag.Diagnostics
	if err := nativeAgentIdentity(item, org, id, previous); err != nil {
		diags.AddError(PO_API_ERR, err.Error())
		return model, diags
	}
	configuration, err := item.RunnerConfiguration.AsNativeAgentRunnerConfiguration()
	if err != nil || configuration.MaxParallelJobs < 1 || !nativePublicKey(configuration.Key) {
		diags.AddError(PO_API_ERR, "Native Runner response has invalid retained key or capacity.")
		return model, diags
	}
	config := nativeAgentRunnerConfiguration{Key: types.StringValue(configuration.Key), Capacity: types.Int64Value(int64(configuration.MaxParallelJobs)),
		Protocols: types.SetNull(types.StringType), Capabilities: types.SetNull(types.StringType), Inventory: jsontypes.NewNormalizedNull()}
	if configuration.SupportedProtocols != nil {
		var d diag.Diagnostics
		config.Protocols, d = types.SetValueFrom(ctx, types.StringType, *configuration.SupportedProtocols)
		diags.Append(d...)
	}
	if configuration.Capabilities != nil {
		var d diag.Diagnostics
		config.Capabilities, d = types.SetValueFrom(ctx, types.StringType, *configuration.Capabilities)
		diags.Append(d...)
	}
	if configuration.NativeExecution != nil {
		raw, e := json.Marshal(configuration.NativeExecution)
		if e != nil {
			diags.AddError(PO_API_ERR, "Native Runner inventory could not be read.")
			return model, diags
		}
		config.Inventory = jsontypes.NewNormalizedValue(string(raw))
	}
	var configDiags diag.Diagnostics
	model.Configuration, configDiags = types.ObjectValueFrom(ctx, nativeAgentConfigurationTypes(), config)
	diags.Append(configDiags...)
	backend, err := parseStateStorageConfigurationResponse(ctx, item.StateStorageConfiguration, commonRunnerStateStorageResourceSchema.Attributes, buildCommonStateStorageModel)
	if err != nil {
		diags.AddError(PO_API_ERR, "Native Runner state backend could not be read.")
		return model, diags
	}
	model.ID, model.UUID, model.Description, model.Backend = types.StringValue(item.Id), types.StringValue(item.Uuid.String()), types.StringPointerValue(item.Description), *backend
	return model, diags
}

func readNativeAgent(ctx context.Context, client cp.ClientWithResponsesInterface, org, id string, previous types.String) (nativeAgentRunnerModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	response, err := client.GetRunnerWithResponse(ctx, org, id)
	if err != nil {
		diags.AddError(PO_CLIENT_ERR, "Unable to read native Runner; existing state is retained.")
		return nativeAgentRunnerModel{}, diags
	}
	if response == nil || response.StatusCode() != http.StatusOK {
		diags.AddError(PO_API_ERR, "Native Runner could not be read; inspect the registration and permissions. Existing state is retained, never automatically recreated.")
		return nativeAgentRunnerModel{}, diags
	}
	return nativeAgentFromAPI(ctx, response.JSON200, org, id, previous)
}

func nativeAgentConfig(ctx context.Context, value types.Object) (nativeAgentRunnerConfiguration, diag.Diagnostics) {
	var configuration nativeAgentRunnerConfiguration
	diags := value.As(ctx, &configuration, basetypes.ObjectAsOptions{})
	return configuration, diags
}

func nativeInventoryEqual(ctx context.Context, a, b jsontypes.Normalized) (bool, diag.Diagnostics) {
	if a.IsUnknown() || b.IsUnknown() {
		var d diag.Diagnostics
		d.AddError(PO_INPUT_ERR, "Native Runner inventory must resolve before applying.")
		return false, d
	}
	if a.IsNull() || b.IsNull() {
		return a.IsNull() && b.IsNull(), nil
	}
	return a.StringSemanticEquals(ctx, b)
}

func nativeAgentModelsEqual(ctx context.Context, a, b nativeAgentRunnerModel) (bool, diag.Diagnostics) {
	left, d := nativeAgentConfig(ctx, a.Configuration)
	right, e := nativeAgentConfig(ctx, b.Configuration)
	d.Append(e...)
	if d.HasError() {
		return false, d
	}
	inventory, e := nativeInventoryEqual(ctx, left.Inventory, right.Inventory)
	d.Append(e...)
	for _, pair := range []struct{ left, right attr.Value }{{a.ID, b.ID}, {a.UUID, b.UUID}, {a.Description, b.Description}, {a.Backend, b.Backend}, {left.Key, right.Key}, {left.Capacity, right.Capacity}, {left.Protocols, right.Protocols}, {left.Capabilities, right.Capabilities}} {
		if !pair.left.Equal(pair.right) {
			return false, d
		}
	}
	return inventory, d
}

func nativeInventoryPatch(ctx context.Context, old, next jsontypes.Normalized) (json.RawMessage, bool, diag.Diagnostics) {
	equal, d := nativeInventoryEqual(ctx, old, next)
	if equal || d.HasError() {
		return nil, false, d
	}
	if next.IsNull() {
		return json.RawMessage("null"), true, d
	}
	return json.RawMessage(next.ValueString()), true, d
}

func nativeAgentConfigurationPatch(ctx context.Context, old, next nativeAgentRunnerConfiguration) (*cp.RunnerConfigurationUpdate, diag.Diagnostics) {
	var d diag.Diagnostics
	fields := map[string]any{}
	if !old.Key.Equal(next.Key) {
		fields["key"] = next.Key.ValueString()
	}
	if !old.Capacity.Equal(next.Capacity) {
		fields["max_parallel_jobs"] = next.Capacity.ValueInt64()
	}
	for _, pair := range []struct {
		name      string
		old, next types.Set
	}{{"supported_protocols", old.Protocols, next.Protocols}, {"capabilities", old.Capabilities, next.Capabilities}} {
		if !pair.old.Equal(pair.next) {
			var values []string
			d.Append(pair.next.ElementsAs(ctx, &values, false)...)
			fields[pair.name] = append([]string{}, values...)
		}
	}
	inventory, changed, e := nativeInventoryPatch(ctx, old.Inventory, next.Inventory)
	d.Append(e...)
	if changed {
		fields["native_execution"] = inventory
	}
	if len(fields) == 0 || d.HasError() {
		return nil, d
	}
	fields["type"] = cp.RunnerTypeNativeAgent
	raw, err := json.Marshal(fields)
	if err != nil {
		d.AddError(PO_INPUT_ERR, "Native Runner PATCH could not be encoded.")
		return nil, d
	}
	configuration := &cp.RunnerConfigurationUpdate{}
	if err := configuration.UnmarshalJSON(raw); err != nil {
		d.AddError(PO_INPUT_ERR, "Native Runner PATCH could not be encoded.")
	}
	return configuration, d
}

func nativeAgentBackend(ctx context.Context, value types.Object) (cp.StateStorageConfiguration, error) {
	kind, ok := value.Attributes()["type"].(types.String)
	if !ok || kind.IsUnknown() {
		return cp.StateStorageConfiguration{}, fmt.Errorf("state backend type must be resolved")
	}
	for name, attribute := range value.Attributes() {
		if name != "type" && name != kind.ValueString()+"_configuration" && !attribute.IsNull() {
			return cp.StateStorageConfiguration{}, fmt.Errorf("only the selected state backend configuration may be provided")
		}
	}
	return createStateStorageConfigurationFromObject(ctx, value)
}

func nativeAgentPatch(ctx context.Context, previous, planned nativeAgentRunnerModel) (cp.RunnerUpdateBody, bool, diag.Diagnostics) {
	var body cp.RunnerUpdateBody
	old, d := nativeAgentConfig(ctx, previous.Configuration)
	next, e := nativeAgentConfig(ctx, planned.Configuration)
	d.Append(e...)
	if d.HasError() {
		return body, false, d
	}
	body.RunnerConfiguration, e = nativeAgentConfigurationPatch(ctx, old, next)
	d.Append(e...)
	if !previous.Description.Equal(planned.Description) {
		if planned.Description.IsNull() {
			d.AddError(PO_INPUT_ERR, "Runner description cannot be cleared to null; use an explicit empty string.")
		} else {
			value := planned.Description.ValueString()
			body.Description = &value
		}
	}
	if !previous.Backend.Equal(planned.Backend) {
		backend, err := nativeAgentBackend(ctx, planned.Backend)
		if err != nil {
			d.AddError(PO_INPUT_ERR, "Native Runner state backend is invalid.")
		} else {
			body.StateStorageConfiguration = &backend
		}
	}
	return body, body.Description != nil || body.RunnerConfiguration != nil || body.StateStorageConfiguration != nil, d
}
