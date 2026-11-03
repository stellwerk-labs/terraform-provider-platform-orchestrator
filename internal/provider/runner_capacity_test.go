package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

type runnerCapacityCase struct {
	name          string
	resource      func() resource.Resource
	dataSource    func() datasource.DataSource
	configuration string
}

func runnerCapacityCases() []runnerCapacityCase {
	return []runnerCapacityCase{
		{"kubernetes", NewKubernetesRunnerResource, NewKubernetesRunnerDataSource, `{"type":"kubernetes","cluster":{"cluster_data":{"certificate_authority_data":"ca","server":"https://cluster"},"auth":{}},"job":{"namespace":"default","service_account":"runner"}}`},
		{"agent", NewKubernetesAgentRunnerResource, NewKubernetesAgentRunnerDataSource, `{"type":"kubernetes-agent","key":"key","job":{"namespace":"default","service_account":"runner"}}`},
		{"eks", NewKubernetesEksRunnerResource, NewKubernetesEksRunnerDataSource, `{"type":"kubernetes-eks","cluster":{"name":"eks","region":"eu-central-1","auth":{"role_arn":"arn:aws:iam::123456789012:role/runner"}},"job":{"namespace":"default","service_account":"runner"}}`},
		{"gke", NewKubernetesGkeRunnerResource, NewKubernetesGkeRunnerDataSource, `{"type":"kubernetes-gke","cluster":{"name":"gke","project_id":"project","location":"europe-west1","auth":{"gcp_audience":"audience","gcp_service_account":"account"}},"job":{"namespace":"default","service_account":"runner"}}`},
		{"ecs", NewServerlessEcsRunnerResource, NewServerlessEcsRunnerDataSource, `{"type":"serverless-ecs","auth":{"role_arn":"arn:aws:iam::123456789012:role/runner"},"job":{"region":"eu-central-1","cluster":"ecs","subnets":["subnet-1"],"security_groups":["sg-1"],"execution_role_arn":"arn:aws:iam::123456789012:role/execution","task_role_arn":"arn:aws:iam::123456789012:role/task","image":"runner:local","environment":{},"secrets":{}}}`},
	}
}
func runnerCapacityFixture(t *testing.T, tc runnerCapacityCase, patch map[string]json.RawMessage) cp.Runner {
	t.Helper()
	raw, err := mergeRunnerCapacity([]byte(tc.configuration), patch)
	require.NoError(t, err)
	var item cp.Runner
	item.Id = "runner"
	require.NoError(t, item.RunnerConfiguration.UnmarshalJSON(raw))
	require.NoError(t, item.StateStorageConfiguration.UnmarshalJSON([]byte(`{"type":"s3","bucket":"state-bucket"}`)))
	return item
}
func runnerJSONFields(t *testing.T, value json.Marshaler) map[string]json.RawMessage {
	t.Helper()
	raw, err := value.MarshalJSON()
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	return fields
}
func TestRunnerCapacityAllFamiliesSchemaAndReadRoundtrip(t *testing.T) {
	for _, tc := range runnerCapacityCases() {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := tc.resource().(*commonRunnerResource)
			require.True(t, ok)
			d, ok := tc.dataSource().(*commonRunnerDataSource)
			require.True(t, ok)
			configuration, ok := r.SchemaDef.Attributes["runner_configuration"].(schema.SingleNestedAttribute)
			require.True(t, ok)
			limit, ok := configuration.Attributes[maxParallelJobsAttribute].(schema.Int64Attribute)
			require.True(t, ok)
			require.True(t, limit.Optional && limit.Computed)
			protocols, ok := configuration.Attributes[supportedProtocolsAttribute].(schema.SetAttribute)
			require.True(t, ok)
			require.True(t, protocols.Optional && protocols.Computed)
			dsConfiguration, ok := d.SchemaDef.Attributes["runner_configuration"].(dsschema.SingleNestedAttribute)
			require.True(t, ok)
			require.True(t, dsConfiguration.Attributes[maxParallelJobsAttribute].IsComputed())
			require.True(t, dsConfiguration.Attributes[supportedProtocolsAttribute].IsComputed())
			for _, patch := range []map[string]json.RawMessage{{}, {maxParallelJobsAttribute: json.RawMessage("2"), supportedProtocolsAttribute: json.RawMessage(`["environment-observation/v1"]`)}, {supportedProtocolsAttribute: json.RawMessage(`[]`)}} {
				item := runnerCapacityFixture(t, tc, patch)
				model, err := r.ReadApiResponseIntoModel(item, commonRunnerModel{})
				require.NoError(t, err)
				dataModel, err := d.ReadApiResponseIntoModel(item, commonRunnerModel{})
				require.NoError(t, err)
				require.True(t, model.RunnerConfiguration.Equal(dataModel.RunnerConfiguration))
				values := model.RunnerConfiguration.Attributes()
				require.Contains(t, values, maxParallelJobsAttribute)
				require.Contains(t, values, supportedProtocolsAttribute)
				if _, present := patch[maxParallelJobsAttribute]; present {
					require.Equal(t, types.Int64Value(2), values[maxParallelJobsAttribute])
				} else {
					require.True(t, values[maxParallelJobsAttribute].IsNull())
				}
				if _, present := patch[supportedProtocolsAttribute]; present {
					require.False(t, values[supportedProtocolsAttribute].IsNull())
				} else {
					require.True(t, values[supportedProtocolsAttribute].IsNull())
				}
				state := tfsdk.State{Schema: r.SchemaDef}
				require.False(t, state.Set(t.Context(), &model).HasError())
				require.False(t, state.Get(t.Context(), &model).HasError())
				// A second refresh receives the augmented state. The family's existing
				// converter must still preserve its own fields without extra-attribute errors.
				again, err := r.ReadApiResponseIntoModel(item, model)
				require.NoError(t, err)
				require.Equal(t, again.RunnerConfiguration.Attributes()[maxParallelJobsAttribute], model.RunnerConfiguration.Attributes()[maxParallelJobsAttribute])
				base, err := withoutRunnerCapacity(t.Context(), model.RunnerConfiguration)
				require.NoError(t, err)
				create, err := r.ConvertRunnerConfigIntoCreateApi(t.Context(), base)
				require.NoError(t, err)
				common := runnerCapacityValues{MaxParallelJobs: types.Int64Null(), SupportedProtocols: types.SetNull(types.StringType)}
				if len(patch) > 0 {
					common.MaxParallelJobs = types.Int64Value(2)
					common.SupportedProtocols = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("environment-observation/v1")})
				}
				create, err = applyRunnerCapacityCreate(t.Context(), create, common)
				require.NoError(t, err)
				fields := runnerJSONFields(t, create)
				require.JSONEq(t, string(patchType(t, item.RunnerConfiguration)), string(fields["type"]))
				if len(patch) == 0 {
					require.NotContains(t, fields, maxParallelJobsAttribute)
					require.NotContains(t, fields, supportedProtocolsAttribute)
				} else {
					require.JSONEq(t, "2", string(fields[maxParallelJobsAttribute]))
					require.JSONEq(t, `["environment-observation/v1"]`, string(fields[supportedProtocolsAttribute]))
				}
				update, err := r.ConvertRunnerConfigIntoUpdateApi(t.Context(), base)
				require.NoError(t, err)
				update, err = applyRunnerCapacityUpdate(t.Context(), update, runnerCapacityValues{MaxParallelJobs: types.Int64Null(), SupportedProtocols: types.SetNull(types.StringType)}, runnerCapacityOwnership{})
				require.NoError(t, err)
				fields = runnerJSONFields(t, update)
				require.NotContains(t, fields, maxParallelJobsAttribute)
				require.NotContains(t, fields, supportedProtocolsAttribute)
				update, err = applyRunnerCapacityUpdate(t.Context(), update, runnerCapacityValues{MaxParallelJobs: types.Int64Null(), SupportedProtocols: types.SetNull(types.StringType)}, runnerCapacityOwnership{MaxParallelJobs: true, SupportedProtocols: true})
				require.NoError(t, err)
				fields = runnerJSONFields(t, update)
				require.JSONEq(t, "null", string(fields[maxParallelJobsAttribute]))
				require.JSONEq(t, "null", string(fields[supportedProtocolsAttribute]))
			}
		})
	}
}
func patchType(t *testing.T, value cp.RunnerConfiguration) json.RawMessage {
	return runnerJSONFields(t, value)["type"]
}
func TestRunnerCapacityKnownNullEmptyAndInvalidValues(t *testing.T) {
	null := runnerCapacityValues{MaxParallelJobs: types.Int64Null(), SupportedProtocols: types.SetNull(types.StringType)}
	empty := null
	empty.SupportedProtocols = types.SetValueMust(types.StringType, []attr.Value{})
	patch, err := runnerCapacityPatch(t.Context(), empty, true, runnerCapacityOwnership{})
	require.NoError(t, err)
	require.JSONEq(t, "[]", string(patch[supportedProtocolsAttribute]))
	require.NotContains(t, patch, maxParallelJobsAttribute)
	for _, values := range []runnerCapacityValues{{MaxParallelJobs: types.Int64Value(0), SupportedProtocols: null.SupportedProtocols}, {MaxParallelJobs: types.Int64Value(-1), SupportedProtocols: null.SupportedProtocols}, {MaxParallelJobs: types.Int64Unknown(), SupportedProtocols: null.SupportedProtocols}, {MaxParallelJobs: null.MaxParallelJobs, SupportedProtocols: types.SetUnknown(types.StringType)}, {MaxParallelJobs: null.MaxParallelJobs, SupportedProtocols: types.SetValueMust(types.StringType, []attr.Value{types.StringUnknown()})}, {MaxParallelJobs: null.MaxParallelJobs, SupportedProtocols: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("")})}, {MaxParallelJobs: null.MaxParallelJobs, SupportedProtocols: types.SetValueMust(types.StringType, []attr.Value{types.StringValue(strings.Repeat("a", 129))})}} {
		_, err := runnerCapacityPatch(t.Context(), values, true, runnerCapacityOwnership{})
		require.Error(t, err)
	}
	values := null
	values.MaxParallelJobs = types.Int64Value(9223372036854775807)
	patch, err = runnerCapacityPatch(t.Context(), values, true, runnerCapacityOwnership{})
	require.NoError(t, err)
	require.Equal(t, "9223372036854775807", string(patch[maxParallelJobsAttribute]))
}
