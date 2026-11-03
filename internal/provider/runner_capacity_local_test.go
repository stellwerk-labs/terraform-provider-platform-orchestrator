package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/stretchr/testify/require"
)

// This is a contained Terraform protocol/wire fixture, not Core authorization or
// admission evidence. Actual new-provider -> new-Core acceptance is separate.
type runnerCapacityWireFixture struct {
	t       *testing.T
	mu      sync.Mutex
	runner  map[string]any
	updates []map[string]json.RawMessage
}

func (f *runnerCapacityWireFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer local-test-token" {
		http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	collection := "/orgs/local-test-org/runners"
	item := collection + "/capacity-agent"
	if r.URL.Path == collection && r.Method == http.MethodPost {
		var value map[string]any
		if json.NewDecoder(r.Body).Decode(&value) != nil {
			http.Error(w, `{"message":"invalid"}`, 400)
			return
		}
		f.runner = value
		configuration, ok := f.runner["runner_configuration"].(map[string]any)
		if !ok {
			http.Error(w, `{"message":"invalid"}`, 400)
			return
		}
		// Simulate an existing operator policy independently of Terraform ownership.
		configuration[maxParallelJobsAttribute] = 2
		configuration[supportedProtocolsAttribute] = []string{"environment-observation/v1"}
		writeJSON(f.t, w, 201, f.runner)
		return
	}
	if r.URL.Path != item {
		http.Error(w, `{"message":"not found"}`, 404)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if f.runner == nil {
			http.Error(w, `{"message":"not found"}`, 404)
			return
		}
		writeJSON(f.t, w, 200, f.runner)
	case http.MethodPatch:
		var value struct {
			Description               string                     `json:"description"`
			RunnerConfiguration       map[string]json.RawMessage `json:"runner_configuration"`
			StateStorageConfiguration map[string]any             `json:"state_storage_configuration"`
		}
		if json.NewDecoder(r.Body).Decode(&value) != nil {
			http.Error(w, `{"message":"invalid"}`, 400)
			return
		}
		f.updates = append(f.updates, value.RunnerConfiguration)
		configuration, ok := f.runner["runner_configuration"].(map[string]any)
		if !ok {
			http.Error(w, `{"message":"invalid"}`, 400)
			return
		}
		for key, raw := range value.RunnerConfiguration {
			if string(raw) == "null" {
				delete(configuration, key)
				continue
			}
			var field any
			if json.Unmarshal(raw, &field) != nil {
				http.Error(w, `{"message":"invalid"}`, 400)
				return
			}
			configuration[key] = field
		}
		f.runner["description"] = value.Description
		f.runner["state_storage_configuration"] = value.StateStorageConfiguration
		writeJSON(f.t, w, 200, f.runner)
	case http.MethodDelete:
		f.runner = nil
		w.WriteHeader(204)
	default:
		http.Error(w, `{"message":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}
func (f *runnerCapacityWireFixture) operatorPolicy() {
	f.mu.Lock()
	defer f.mu.Unlock()
	configuration, ok := f.runner["runner_configuration"].(map[string]any)
	require.True(f.t, ok)
	configuration[maxParallelJobsAttribute] = 4
	configuration[supportedProtocolsAttribute] = []string{"operator/v1"}
}
func capacityStateChecks(limit knownvalue.Check, protocols knownvalue.Check) []statecheck.StateCheck {
	var checks []statecheck.StateCheck
	for _, name := range []string{"platform-orchestrator_kubernetes_agent_runner.test", "data.platform-orchestrator_kubernetes_agent_runner.test"} {
		checks = append(checks, statecheck.ExpectKnownValue(name, tfjsonpath.New("runner_configuration").AtMapKey(maxParallelJobsAttribute), limit), statecheck.ExpectKnownValue(name, tfjsonpath.New("runner_configuration").AtMapKey(supportedProtocolsAttribute), protocols))
	}
	return checks
}
func TestAccRunnerCapacityOwnershipLocal(t *testing.T) {
	fixture := &runnerCapacityWireFixture{t: t}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	configuration := func(description, common string) string { return runnerCapacityHCL(server.URL, description, common) }
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: []resource.TestStep{
		{Config: configuration("Legacy omitted fields", ""), ConfigStateChecks: capacityStateChecks(knownvalue.Int64Exact(2), knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("environment-observation/v1")}))},
		{Config: configuration("Unrelated update preserves operator policy", ""), ConfigStateChecks: capacityStateChecks(knownvalue.Int64Exact(2), knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("environment-observation/v1")}))},
		// A same-value no-op apply cannot persist new private ownership.
		// Removing that assertion must preserve the operator policy.
		{Config: configuration("Unrelated update preserves operator policy", `max_parallel_jobs = 2
 supported_protocols = ["environment-observation/v1"]`), ConfigStateChecks: capacityStateChecks(knownvalue.Int64Exact(2), knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("environment-observation/v1")}))},
		{Config: configuration("Unrelated update preserves operator policy", ""), ConfigStateChecks: capacityStateChecks(knownvalue.Int64Exact(2), knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("environment-observation/v1")}))},
		{Config: configuration("Terraform claims capacity", `max_parallel_jobs = 3
 supported_protocols = ["environment-observation/v1"]`), ConfigStateChecks: capacityStateChecks(knownvalue.Int64Exact(3), knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("environment-observation/v1")}))},
		{Config: configuration("Terraform claims capacity", ""), ConfigStateChecks: capacityStateChecks(knownvalue.Null(), knownvalue.Null())},
		{PreConfig: fixture.operatorPolicy, Config: configuration("Ownership released, operator policy preserved", ""), ConfigStateChecks: capacityStateChecks(knownvalue.Int64Exact(4), knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("operator/v1")}))},
		{ResourceName: "platform-orchestrator_kubernetes_agent_runner.test", ImportState: true, ImportStateVerify: true},
		{Config: configuration("Declare no protocols", `supported_protocols = []`), ConfigStateChecks: capacityStateChecks(knownvalue.Int64Exact(4), knownvalue.SetSizeExact(0))},
		{Config: configuration("Declare no protocols", ""), ConfigStateChecks: capacityStateChecks(knownvalue.Int64Exact(4), knownvalue.Null())},
	}})
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.Len(t, fixture.updates, 6)
	require.NotContains(t, fixture.updates[0], maxParallelJobsAttribute)
	require.NotContains(t, fixture.updates[0], supportedProtocolsAttribute)
	require.JSONEq(t, "3", string(fixture.updates[1][maxParallelJobsAttribute]))
	require.JSONEq(t, "null", string(fixture.updates[2][maxParallelJobsAttribute]))
	require.JSONEq(t, "null", string(fixture.updates[2][supportedProtocolsAttribute]))
	require.NotContains(t, fixture.updates[3], maxParallelJobsAttribute)
	require.NotContains(t, fixture.updates[3], supportedProtocolsAttribute)
	require.NotContains(t, fixture.updates[4], maxParallelJobsAttribute)
	require.JSONEq(t, "[]", string(fixture.updates[4][supportedProtocolsAttribute]))
	require.NotContains(t, fixture.updates[5], maxParallelJobsAttribute)
	require.JSONEq(t, "null", string(fixture.updates[5][supportedProtocolsAttribute]))
}
func runnerCapacityHCL(origin, description, common string) string {
	return fmt.Sprintf(`
provider "platform-orchestrator" {
 api_url = %q
 org_id = "local-test-org"
 auth_token = "local-test-token"
}
resource "platform-orchestrator_kubernetes_agent_runner" "test" {
 id = "capacity-agent"
 description = %q
 runner_configuration = {
  %s
  key = <<-KEY
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAc5dgCx4ano39JT0XgTsHnts3jej+5xl7ZAwSIrKpef0=
-----END PUBLIC KEY-----
KEY
  job = {
   namespace = "default"
   service_account = "runner"
  }
 }
 state_storage_configuration = {
  type = "kubernetes"
  kubernetes_configuration = { namespace = "runner-state" }
 }
}
data "platform-orchestrator_kubernetes_agent_runner" "test" {
 id = platform-orchestrator_kubernetes_agent_runner.test.id
}
`, origin, description, strings.TrimSpace(common))
}
