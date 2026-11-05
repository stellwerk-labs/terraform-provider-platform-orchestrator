package provider

import (
	"context"
	"encoding/json"
	"errors"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

const runnerCapacityPrivateKey = "runner_capacity_ownership_v1"
const maxParallelJobsAttribute = "max_parallel_jobs"
const supportedProtocolsAttribute = "supported_protocols"
const runnerCapacityDescription = "Maximum concurrent jobs across all Environments using this Runner. Must be positive. Omission preserves Core configuration (legacy unlimited when unset). Removing a value previously applied by Terraform clears the limit. Same-value no-op adoption does not establish management; apply a real configured update to claim an imported/operator value. Reducing the limit drains existing jobs before new admission."
const runnerProtocolsDescription = "Protocols explicitly supported by the configured Runner image, at most 16 unique names of 1 to 128 characters. Omission preserves Core configuration. Removing a value previously applied by Terraform clears the opt-in; an empty set declares no protocols. Observation requires environment-observation/v1."

type runnerCapacityOwnership struct {
	MaxParallelJobs    bool `json:"max_parallel_jobs"`
	SupportedProtocols bool `json:"supported_protocols"`
}
type runnerCapacityValues struct {
	MaxParallelJobs    types.Int64
	SupportedProtocols types.Set
}
type runnerCapacityPrivate interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
	SetKey(context.Context, string, []byte) diag.Diagnostics
}

func withRunnerCapacityResource(r *commonRunnerResource) *commonRunnerResource {
	attributes := maps.Clone(r.SchemaDef.Attributes)
	configuration, ok := attributes["runner_configuration"].(schema.SingleNestedAttribute)
	if !ok {
		panic("Runner resource configuration must be a nested attribute")
	}
	configuration.Attributes = maps.Clone(configuration.Attributes)
	configuration.Attributes[maxParallelJobsAttribute] = schema.Int64Attribute{MarkdownDescription: runnerCapacityDescription, Optional: true, Computed: true, Validators: []validator.Int64{int64validator.AtLeast(1)}}
	configuration.Attributes[supportedProtocolsAttribute] = schema.SetAttribute{MarkdownDescription: runnerProtocolsDescription, Optional: true, Computed: true, ElementType: types.StringType, Validators: []validator.Set{setvalidator.SizeAtMost(16), setvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 128))}}
	attributes["runner_configuration"] = configuration
	r.SchemaDef.Attributes = attributes
	r.ReadApiResponseIntoModel = withRunnerCapacityRead(r.ReadApiResponseIntoModel)
	return r
}
func withRunnerCapacityDataSource(r *commonRunnerDataSource) *commonRunnerDataSource {
	attributes := maps.Clone(r.SchemaDef.Attributes)
	configuration, ok := attributes["runner_configuration"].(dsschema.SingleNestedAttribute)
	if !ok {
		panic("Runner data source configuration must be a nested attribute")
	}
	configuration.Attributes = maps.Clone(configuration.Attributes)
	configuration.Attributes[maxParallelJobsAttribute] = dsschema.Int64Attribute{MarkdownDescription: runnerCapacityDescription, Computed: true}
	configuration.Attributes[supportedProtocolsAttribute] = dsschema.SetAttribute{MarkdownDescription: runnerProtocolsDescription, Computed: true, ElementType: types.StringType}
	attributes["runner_configuration"] = configuration
	r.SchemaDef.Attributes = attributes
	r.ReadApiResponseIntoModel = withRunnerCapacityRead(r.ReadApiResponseIntoModel)
	return r
}

// Existing family-specific converters continue owning cluster, credentials, job
// and state storage. Only these generic configuration fields are added here.
func withoutRunnerCapacity(ctx context.Context, obj types.Object) (types.Object, error) {
	if obj.IsNull() || obj.IsUnknown() {
		return obj, nil
	}
	attributes := maps.Clone(obj.Attributes())
	attributeTypes := maps.Clone(obj.AttributeTypes(ctx))
	for _, name := range []string{maxParallelJobsAttribute, supportedProtocolsAttribute} {
		delete(attributes, name)
		delete(attributeTypes, name)
	}
	result, diags := types.ObjectValue(attributeTypes, attributes)
	if diags.HasError() {
		return types.Object{}, errors.New("failed to isolate Runner configuration")
	}
	return result, nil
}
func withRunnerCapacityRead(read func(cp.Runner, commonRunnerModel) (commonRunnerModel, error)) func(cp.Runner, commonRunnerModel) (commonRunnerModel, error) {
	return func(item cp.Runner, prior commonRunnerModel) (commonRunnerModel, error) {
		ctx := context.Background()
		var err error
		prior.RunnerConfiguration, err = withoutRunnerCapacity(ctx, prior.RunnerConfiguration)
		if err != nil {
			return commonRunnerModel{}, err
		}
		result, err := read(item, prior)
		if err != nil {
			return commonRunnerModel{}, err
		}
		raw, err := item.RunnerConfiguration.MarshalJSON()
		if err != nil {
			return commonRunnerModel{}, errors.New("invalid Runner configuration response")
		}
		var common struct {
			MaxParallelJobs    *int64    `json:"max_parallel_jobs"`
			SupportedProtocols *[]string `json:"supported_protocols"`
		}
		if json.Unmarshal(raw, &common) != nil {
			return commonRunnerModel{}, errors.New("invalid Runner configuration response")
		}
		values := runnerCapacityValues{MaxParallelJobs: types.Int64PointerValue(common.MaxParallelJobs), SupportedProtocols: types.SetNull(types.StringType)}
		if common.SupportedProtocols != nil {
			var diags diag.Diagnostics
			values.SupportedProtocols, diags = types.SetValueFrom(ctx, types.StringType, *common.SupportedProtocols)
			if diags.HasError() {
				return commonRunnerModel{}, errors.New("invalid Runner protocols response")
			}
		}
		if _, err = runnerCapacityPatch(ctx, values, true, runnerCapacityOwnership{}); err != nil {
			return commonRunnerModel{}, errors.New("invalid Runner capacity response")
		}
		attributes := maps.Clone(result.RunnerConfiguration.Attributes())
		attributeTypes := maps.Clone(result.RunnerConfiguration.AttributeTypes(ctx))
		attributes[maxParallelJobsAttribute] = values.MaxParallelJobs
		attributeTypes[maxParallelJobsAttribute] = types.Int64Type
		attributes[supportedProtocolsAttribute] = values.SupportedProtocols
		attributeTypes[supportedProtocolsAttribute] = types.SetType{ElemType: types.StringType}
		var diags diag.Diagnostics
		result.RunnerConfiguration, diags = types.ObjectValue(attributeTypes, attributes)
		if diags.HasError() {
			return commonRunnerModel{}, errors.New("failed to retain Runner capacity state")
		}
		return result, nil
	}
}

func runnerCapacityFromConfig(ctx context.Context, config tfsdk.Config) (runnerCapacityValues, diag.Diagnostics) {
	var values runnerCapacityValues
	var diags diag.Diagnostics
	diags.Append(config.GetAttribute(ctx, path.Root("runner_configuration").AtName(maxParallelJobsAttribute), &values.MaxParallelJobs)...)
	diags.Append(config.GetAttribute(ctx, path.Root("runner_configuration").AtName(supportedProtocolsAttribute), &values.SupportedProtocols)...)
	return values, diags
}
func readRunnerCapacityOwnership(ctx context.Context, private runnerCapacityPrivate) (runnerCapacityOwnership, diag.Diagnostics) {
	var value runnerCapacityOwnership
	raw, diags := private.GetKey(ctx, runnerCapacityPrivateKey)
	if diags.HasError() || len(raw) == 0 {
		return value, diags
	}
	if json.Unmarshal(raw, &value) != nil {
		diags.AddError(PO_PROVIDER_ERR, "Runner capacity ownership state is invalid.")
	}
	return value, diags
}
func writeRunnerCapacityOwnership(ctx context.Context, private runnerCapacityPrivate, values runnerCapacityValues) diag.Diagnostics {
	value := runnerCapacityOwnership{MaxParallelJobs: !values.MaxParallelJobs.IsNull(), SupportedProtocols: !values.SupportedProtocols.IsNull()}
	return storeRunnerCapacityOwnership(ctx, private, value)
}
func storeRunnerCapacityOwnership(ctx context.Context, private runnerCapacityPrivate, value runnerCapacityOwnership) diag.Diagnostics {
	raw, err := json.Marshal(value)
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError(PO_PROVIDER_ERR, "Failed to encode Runner capacity ownership.")
		return diags
	}
	return private.SetKey(ctx, runnerCapacityPrivateKey, raw)
}

// A configured null and an omitted value are equivalent in Terraform. Private
// ownership distinguishes removing a previously managed value from a legacy or
// imported configuration that has never claimed an operator's Core setting.
func (r *commonRunnerResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	owned, diags := readRunnerCapacityOwnership(ctx, req.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	configured, diags := runnerCapacityFromConfig(ctx, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if owned.MaxParallelJobs && configured.MaxParallelJobs.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("runner_configuration").AtName(maxParallelJobsAttribute), types.Int64Null())...)
	}
	if owned.SupportedProtocols && configured.SupportedProtocols.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("runner_configuration").AtName(supportedProtocolsAttribute), types.SetNull(types.StringType))...)
	}
}

func runnerCapacityPatch(ctx context.Context, values runnerCapacityValues, create bool, owned runnerCapacityOwnership) (map[string]json.RawMessage, error) {
	if values.MaxParallelJobs.IsUnknown() || values.SupportedProtocols.IsUnknown() {
		return nil, errors.New("runner capacity and protocols must be known before applying")
	}
	result := map[string]json.RawMessage{}
	if !values.MaxParallelJobs.IsNull() {
		n := values.MaxParallelJobs.ValueInt64()
		if n < 1 {
			return nil, errors.New("max_parallel_jobs must be positive")
		}
		raw, err := json.Marshal(n)
		if err != nil {
			return nil, err
		}
		result[maxParallelJobsAttribute] = raw
	} else if !create && owned.MaxParallelJobs {
		result[maxParallelJobsAttribute] = json.RawMessage("null")
	}
	if !values.SupportedProtocols.IsNull() {
		var protocols []string
		diags := values.SupportedProtocols.ElementsAs(ctx, &protocols, false)
		if diags.HasError() || len(protocols) > 16 {
			return nil, errors.New("supported_protocols must contain at most 16 known names")
		}
		seen := map[string]bool{}
		for _, protocol := range protocols {
			if len(protocol) < 1 || len(protocol) > 128 || seen[protocol] {
				return nil, errors.New("supported_protocols must contain bounded unique names")
			}
			seen[protocol] = true
		}
		if protocols == nil {
			protocols = []string{}
		}
		raw, err := json.Marshal(protocols)
		if err != nil {
			return nil, err
		}
		result[supportedProtocolsAttribute] = raw
	} else if !create && owned.SupportedProtocols {
		result[supportedProtocolsAttribute] = json.RawMessage("null")
	}
	return result, nil
}

func mergeRunnerCapacity(raw []byte, patch map[string]json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("invalid generated Runner configuration")
	}
	for _, name := range []string{maxParallelJobsAttribute, supportedProtocolsAttribute} {
		delete(fields, name)
	}
	maps.Copy(fields, patch)
	return json.Marshal(fields)
}
func applyRunnerCapacityCreate(ctx context.Context, value cp.RunnerConfiguration, common runnerCapacityValues) (cp.RunnerConfiguration, error) {
	patch, err := runnerCapacityPatch(ctx, common, true, runnerCapacityOwnership{})
	if err != nil {
		return cp.RunnerConfiguration{}, err
	}
	raw, err := value.MarshalJSON()
	if err != nil {
		return cp.RunnerConfiguration{}, err
	}
	raw, err = mergeRunnerCapacity(raw, patch)
	if err != nil {
		return cp.RunnerConfiguration{}, err
	}
	err = value.UnmarshalJSON(raw)
	return value, err
}
func applyRunnerCapacityUpdate(ctx context.Context, value cp.RunnerConfigurationUpdate, common runnerCapacityValues, owned runnerCapacityOwnership) (cp.RunnerConfigurationUpdate, error) {
	patch, err := runnerCapacityPatch(ctx, common, false, owned)
	if err != nil {
		return cp.RunnerConfigurationUpdate{}, err
	}
	raw, err := value.MarshalJSON()
	if err != nil {
		return cp.RunnerConfigurationUpdate{}, err
	}
	raw, err = mergeRunnerCapacity(raw, patch)
	if err != nil {
		return cp.RunnerConfigurationUpdate{}, err
	}
	err = value.UnmarshalJSON(raw)
	return value, err
}

var _ resource.ResourceWithModifyPlan = &commonRunnerResource{}
