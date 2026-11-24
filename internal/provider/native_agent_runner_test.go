package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
	"github.com/stretchr/testify/require"
)

const nativeTestOrg = "native-test-org"
const nativeTestID = "native_pool"
const nativeTestUUID = "719870ca-5494-4ac2-8850-fef3b3472111"
const nativeTestKey = "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAc5dgCx4ano39JT0XgTsHnts3jej+5xl7ZAwSIrKpef0=\n-----END PUBLIC KEY-----"
const nativeTestInventory = `{"contract_version":"stellwerk.runner.catalogue/v1","runtime_version":"stellwerk.runner.rollout/v2","tools_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tools":[]}`

// Owned consumer protocol fixture. It is deliberately not Core business-policy,
// PostgreSQL retention, IAM or actual native execution qualification.
type nativeAgentWireFixture struct {
	mu                      sync.Mutex
	runner                  map[string]any
	patches                 []map[string]json.RawMessage
	creates                 int
	getStatus, deleteStatus int
}

func nativeAgentFixtureRunner(t *testing.T) map[string]any {
	t.Helper()
	var inventory map[string]any
	require.NoError(t, json.Unmarshal([]byte(nativeTestInventory), &inventory))
	return map[string]any{"id": nativeTestID, "org_id": nativeTestOrg, "uuid": nativeTestUUID, "created_at": "2026-11-20T10:00:00Z", "updated_at": "2026-11-20T10:00:00Z", "description": "initial",
		"runner_configuration":        map[string]any{"type": "native-agent", "key": nativeTestKey, "max_parallel_jobs": float64(2), "supported_protocols": []any{"stellwerk.pipeline/v2", "stellwerk.pipeline.plan-apply/v2"}, "capabilities": []any{}, "native_execution": inventory},
		"state_storage_configuration": map[string]any{"type": "s3", "bucket": "native-state"}}
}
func (f *nativeAgentWireFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	collection := "/orgs/" + nativeTestOrg + "/runners"
	if r.URL.Path == collection && r.Method == http.MethodPost {
		f.creates++
		var value map[string]any
		if json.NewDecoder(r.Body).Decode(&value) != nil {
			w.WriteHeader(400)
			return
		}
		value["org_id"], value["uuid"], value["created_at"], value["updated_at"] = nativeTestOrg, nativeTestUUID, "2026-11-20T10:00:00Z", "2026-11-20T10:00:00Z"
		f.runner = value
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(f.runner)
		return
	}
	if r.URL.Path != collection+"/"+nativeTestID {
		w.WriteHeader(404)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if f.getStatus != 0 {
			w.WriteHeader(f.getStatus)
			return
		}
	case http.MethodPatch:
		if !f.applyPatch(r) {
			w.WriteHeader(400)
			return
		}
	case http.MethodDelete:
		status := f.deleteStatus
		if status == 0 {
			status = 409
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "registration retained"})
		return
	default:
		w.WriteHeader(405)
		return
	}
	if f.runner == nil {
		w.WriteHeader(404)
		return
	}
	_ = json.NewEncoder(w).Encode(f.runner)
}
func (f *nativeAgentWireFixture) applyPatch(r *http.Request) bool {
	var fields map[string]json.RawMessage
	if json.NewDecoder(r.Body).Decode(&fields) != nil {
		return false
	}
	f.patches = append(f.patches, fields)
	if config, exists := fields["runner_configuration"]; exists {
		if !f.applyConfiguration(config) {
			return false
		}
	}
	delete(fields, "runner_configuration")
	for name, raw := range fields {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return false
		}
		f.runner[name] = value
	}
	return true
}
func (f *nativeAgentWireFixture) applyConfiguration(raw json.RawMessage) bool {
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	current, ok := f.runner["runner_configuration"].(map[string]any)
	if !ok {
		return false
	}
	for name, value := range fields {
		if value == nil {
			delete(current, name)
		} else {
			current[name] = value
		}
	}
	return true
}
func nativeFixtureConfiguration(t *testing.T, runner map[string]any) map[string]any {
	t.Helper()
	configuration, ok := runner["runner_configuration"].(map[string]any)
	require.True(t, ok)
	return configuration
}
func nativeAgentTestResource(t *testing.T, f *nativeAgentWireFixture) (*NativeAgentRunnerResource, nativeAgentRunnerModel, tfsdk.State) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(server.Close)
	client, err := cp.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	r := &NativeAgentRunnerResource{client: client, org: nativeTestOrg}
	var schema resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &schema)
	raw, err := json.Marshal(nativeAgentFixtureRunner(t))
	require.NoError(t, err)
	var runner cp.Runner
	require.NoError(t, json.Unmarshal(raw, &runner))
	model, diags := nativeAgentFromAPI(t.Context(), &runner, nativeTestOrg, nativeTestID, types.StringNull())
	require.False(t, diags.HasError(), diags)
	state := tfsdk.State{Schema: schema.Schema}
	require.False(t, state.Set(t.Context(), &model).HasError())
	return r, model, state
}
func nativeAgentModelInventory(t *testing.T, m nativeAgentRunnerModel, value jsontypes.Normalized) nativeAgentRunnerModel {
	t.Helper()
	configuration, d := nativeAgentConfig(t.Context(), m.Configuration)
	require.False(t, d.HasError(), d)
	configuration.Inventory = value
	m.Configuration, d = types.ObjectValueFrom(t.Context(), nativeAgentConfigurationTypes(), configuration)
	require.False(t, d.HasError(), d)
	return m
}
func TestNativeAgentCatalogueStrictPublicKeys(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"valid", nativeTestInventory, true},
		{"export envelope is not inner inventory", `{"supported_protocols":[],"capabilities":[],"native_execution":` + nativeTestInventory + `}`, false},
		{"null document", "null", false},
		{"array document", "[]", false},
		{"oversized", strings.Repeat(" ", 64<<10) + nativeTestInventory, false},
		{"legacy runtime", strings.Replace(nativeTestInventory, "rollout/v2", "rollout/v1", 1), false},
		{"invalid digest", strings.Replace(nativeTestInventory, strings.Repeat("a", 64), strings.Repeat("g", 64), 1), false},
		{"case alias", strings.Replace(nativeTestInventory, "tools_digest", "TOOLS_DIGEST", 1), false},
		{"Unicode alias", strings.Replace(nativeTestInventory, "tools_digest", "toolſ_digest", 1), false},
		{"private field", strings.Replace(nativeTestInventory, `"tools":[]`, `"tools":[],"postgres":{"dsn":"private-canary"}`, 1), false},
		{"trailing document", nativeTestInventory + ` {}`, false},
		{"Unicode value", strings.Replace(nativeTestInventory, `"tools":[]`, `"tools":[{"id":"ddl","name":"Datenbank prüfen","phase":"post_apply","retry":"receipt","required_capabilities":[]}]`, 1), true},
		{"nested alias", strings.Replace(nativeTestInventory, `"tools":[]`, `"tools":[{"id":"ddl","NAME":"DDL","phase":"post_apply","retry":"receipt","required_capabilities":[]}]`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := nativeCatalogue(tc.input)
			require.Equal(t, tc.valid, err == nil)
			if err != nil {
				require.NotContains(t, err.Error(), "private-canary")
			}
		})
	}
}
func TestNativeAgentPlanNullUnknownAndDescription(t *testing.T) {
	f := &nativeAgentWireFixture{}
	r, model, state := nativeAgentTestResource(t, f)
	empty := tfsdk.State{Schema: state.Schema, Raw: tftypes.NewValue(state.Schema.Type().TerraformType(t.Context()), nil)}
	for _, tc := range []struct {
		name                            string
		inventory                       jsontypes.Normalized
		existing, descriptionNull, fail bool
	}{
		{name: "new null", inventory: jsontypes.NewNormalizedNull(), fail: true},
		{name: "new unknown", inventory: jsontypes.NewNormalizedUnknown()},
		{name: "existing null", inventory: jsontypes.NewNormalizedNull(), existing: true},
		{name: "existing unknown", inventory: jsontypes.NewNormalizedUnknown(), existing: true},
		{name: "description removal", inventory: jsontypes.NewNormalizedValue(nativeTestInventory), existing: true, descriptionNull: true, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planned := nativeAgentModelInventory(t, model, tc.inventory)
			if tc.descriptionNull {
				planned.Description = types.StringNull()
			}
			plan := tfsdk.Plan{Schema: state.Schema}
			require.False(t, plan.Set(t.Context(), &planned).HasError())
			previous := empty
			if tc.existing {
				previous = state
			}
			response := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(t.Context(), resource.ModifyPlanRequest{Plan: plan, State: previous}, &response)
			require.Equal(t, tc.fail, response.Diagnostics.HasError(), response.Diagnostics)
		})
	}
	destroy := resource.ModifyPlanResponse{}
	r.ModifyPlan(t.Context(), resource.ModifyPlanRequest{Plan: tfsdk.Plan{Schema: state.Schema, Raw: empty.Raw}, State: state}, &destroy)
	require.False(t, destroy.Diagnostics.HasError())
}
func TestNativeAgentPatchOmissionNullAndSemanticEquality(t *testing.T) {
	_, model, _ := nativeAgentTestResource(t, &nativeAgentWireFixture{})
	for _, tc := range []struct {
		name        string
		inventory   jsontypes.Normalized
		description string
		expected    string
	}{
		{"description only", jsontypes.NewNormalizedValue(nativeTestInventory), "changed", `{"description":"changed"}`},
		{"empty description", jsontypes.NewNormalizedValue(nativeTestInventory), "", `{"description":""}`},
		{"clear", jsontypes.NewNormalizedNull(), "initial", `{"runner_configuration":{"type":"native-agent","native_execution":null}}`},
		{"JSON formatting", jsontypes.NewNormalizedValue("\n" + nativeTestInventory + "\n"), "initial", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planned := nativeAgentModelInventory(t, model, tc.inventory)
			planned.Description = types.StringValue(tc.description)
			body, _, diags := nativeAgentPatch(t.Context(), model, planned)
			require.False(t, diags.HasError(), diags)
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			require.JSONEq(t, tc.expected, string(raw))
		})
	}
}
func TestNativeAgentSavedDescriptionUpdateDoesNotRestoreInventory(t *testing.T) {
	f := &nativeAgentWireFixture{runner: nativeAgentFixtureRunner(t)}
	r, planned, state := nativeAgentTestResource(t, f)
	planned.Description = types.StringValue("changed")
	plan := tfsdk.Plan{Schema: state.Schema}
	require.False(t, plan.Set(t.Context(), &planned).HasError())
	delete(nativeFixtureConfiguration(t, f.runner), "native_execution")
	response := resource.UpdateResponse{}
	r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: plan}, &response)
	require.True(t, response.Diagnostics.HasError())
	require.Contains(t, response.Diagnostics.Errors()[0].Summary(), "outside the reviewed plan")
	require.True(t, response.State.Raw.Equal(state.Raw))
	require.Len(t, f.patches, 1)
	require.NotContains(t, f.patches[0], "runner_configuration")
	require.NotContains(t, nativeFixtureConfiguration(t, f.runner), "native_execution")
}
func TestNativeAgentIdentityAttentionRetainsStateBeforeMutation(t *testing.T) {
	for _, change := range []string{"org", "slug", "type", "UUID", "missing UUID", "404"} {
		t.Run(change, func(t *testing.T) {
			f := &nativeAgentWireFixture{runner: nativeAgentFixtureRunner(t)}
			r, planned, state := nativeAgentTestResource(t, f)
			switch change {
			case "org":
				f.runner["org_id"] = "other-org"
			case "slug":
				f.runner["id"] = "other-runner"
			case "type":
				nativeFixtureConfiguration(t, f.runner)["type"] = "kubernetes-agent"
			case "UUID":
				f.runner["uuid"] = "8b8172b2-e003-4567-a969-7b8b0ec59853"
			case "missing UUID":
				delete(f.runner, "uuid")
			case "404":
				f.getStatus = 404
			}
			read := resource.ReadResponse{}
			r.Read(t.Context(), resource.ReadRequest{State: state}, &read)
			require.True(t, read.Diagnostics.HasError())
			require.True(t, read.State.Raw.Equal(state.Raw))
			planned.Description = types.StringValue("changed")
			plan := tfsdk.Plan{Schema: state.Schema}
			require.False(t, plan.Set(t.Context(), &planned).HasError())
			update := resource.UpdateResponse{}
			r.Update(t.Context(), resource.UpdateRequest{State: state, Plan: plan}, &update)
			require.True(t, update.Diagnostics.HasError())
			require.True(t, update.State.Raw.Equal(state.Raw))
			require.Empty(t, f.patches)
		})
	}
}
func TestNativeAgentDeleteAlwaysRetainsTrackedIdentity(t *testing.T) {
	for _, status := range []int{409, 204, 404, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := &nativeAgentWireFixture{runner: nativeAgentFixtureRunner(t), deleteStatus: status}
			r, _, state := nativeAgentTestResource(t, f)
			response := resource.DeleteResponse{}
			r.Delete(t.Context(), resource.DeleteRequest{State: state}, &response)
			require.True(t, response.Diagnostics.HasError())
			require.True(t, response.State.Raw.Equal(state.Raw))
		})
	}
}
func TestNativeAgentCreateRefusesUnresolvedOrPrivateBeforeHTTP(t *testing.T) {
	f := &nativeAgentWireFixture{}
	r, model, state := nativeAgentTestResource(t, f)
	for _, inventory := range []jsontypes.Normalized{jsontypes.NewNormalizedNull(), jsontypes.NewNormalizedUnknown(), jsontypes.NewNormalizedValue(`{"postgres":{"dsn":"private-canary"}}`)} {
		planned := nativeAgentModelInventory(t, model, inventory)
		plan := tfsdk.Plan{Schema: state.Schema}
		require.False(t, plan.Set(t.Context(), &planned).HasError())
		response := resource.CreateResponse{State: tfsdk.State{Schema: state.Schema}}
		r.Create(t.Context(), resource.CreateRequest{Plan: plan}, &response)
		require.True(t, response.Diagnostics.HasError())
		require.NotContains(t, response.Diagnostics.Errors()[0].Detail(), "private-canary")
		require.Zero(t, f.creates)
	}
}

func TestNativeAgentImportAndDataSourceRejectWrongOrganization(t *testing.T) {
	f := &nativeAgentWireFixture{runner: nativeAgentFixtureRunner(t)}
	r, _, state := nativeAgentTestResource(t, f)
	f.runner["org_id"] = "other-org"
	imported := resource.ImportStateResponse{State: tfsdk.State{Schema: state.Schema}}
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: nativeTestID}, &imported)
	require.True(t, imported.Diagnostics.HasError())
	d := &NativeAgentRunnerDataSource{client: r.client, org: r.org}
	var schema datasource.SchemaResponse
	d.Schema(t.Context(), datasource.SchemaRequest{}, &schema)
	response := datasource.ReadResponse{State: tfsdk.State{Schema: schema.Schema}}
	d.Read(t.Context(), datasource.ReadRequest{Config: tfsdk.Config{Schema: schema.Schema, Raw: state.Raw}}, &response)
	require.True(t, response.Diagnostics.HasError())
	require.Empty(t, f.patches)
}
func TestNativeAgentMissingTrackedUUIDIsNotAdopted(t *testing.T) {
	f := &nativeAgentWireFixture{runner: nativeAgentFixtureRunner(t)}
	r, model, state := nativeAgentTestResource(t, f)
	model.UUID = types.StringNull()
	require.False(t, state.Set(t.Context(), &model).HasError())
	response := resource.ReadResponse{}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
	require.True(t, response.Diagnostics.HasError())
	require.True(t, state.Raw.Equal(response.State.Raw))
}

func TestNativeRunnerIDProfileMatchesCore(t *testing.T) {
	document := loadContractDocument(t, coreSpecPath)
	schemas := contractObject(t, contractObject(t, document["components"])["schemas"])
	properties := contractObject(t, contractObject(t, schemas["RunnerCreateBody"])["properties"])
	id := contractObject(t, properties["id"])
	require.Equal(t, id["pattern"], nativeRunnerIDPattern.String())
	require.NotContains(t, id, "maxLength", "historical09e generic Runner IDs are unbounded;128is the separate Native v2 profile")
}
func TestNativeRunnerIDResourceDataSourceAndImportBoundaries(t *testing.T) {
	var calls atomic.Int32
	raw, err := json.Marshal(nativeAgentFixtureRunner(t))
	require.NoError(t, err)
	var original cp.Runner
	require.NoError(t, json.Unmarshal(raw, &original))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		item := original
		item.Id = strings.TrimPrefix(req.URL.Path, "/orgs/"+nativeTestOrg+"/runners/")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item)
	}))
	t.Cleanup(server.Close)
	client, err := cp.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	r := &NativeAgentRunnerResource{client: client, org: nativeTestOrg}
	var resourceSchema resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &resourceSchema)
	d := &NativeAgentRunnerDataSource{}
	var dataSchema datasource.SchemaResponse
	d.Schema(t.Context(), datasource.SchemaRequest{}, &dataSchema)
	resourceID, ok := resourceSchema.Schema.Attributes["id"].(rschema.StringAttribute)
	require.True(t, ok)
	dataID, ok := dataSchema.Schema.Attributes["id"].(dsschema.StringAttribute)
	require.True(t, ok)
	for _, tc := range []struct {
		name, id string
		valid    bool
	}{
		{"2 characters", "a_", true}, {"128 characters", "a" + strings.Repeat("-", 127), true}, {"129 characters", "a" + strings.Repeat("_", 128), false},
		{"underscore", nativeTestID, true}, {"too short", "a", false}, {"other entity syntax", "module.id", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, validators := range [][]validator.String{resourceID.Validators, dataID.Validators} {
				response := validator.StringResponse{}
				for _, v := range validators {
					v.ValidateString(t.Context(), validator.StringRequest{Path: path.Root("id"), ConfigValue: types.StringValue(tc.id)}, &response)
				}
				require.Equal(t, !tc.valid, response.Diagnostics.HasError(), response.Diagnostics)
			}
			before := calls.Load()
			response := resource.ImportStateResponse{State: tfsdk.State{Schema: resourceSchema.Schema}}
			r.ImportState(t.Context(), resource.ImportStateRequest{ID: tc.id}, &response)
			require.Equal(t, !tc.valid, response.Diagnostics.HasError(), response.Diagnostics)
			if tc.valid {
				require.Equal(t, before+1, calls.Load())
			} else {
				require.Equal(t, before, calls.Load(), "invalid Native ID must refuse before GET")
			}
		})
	}
}
