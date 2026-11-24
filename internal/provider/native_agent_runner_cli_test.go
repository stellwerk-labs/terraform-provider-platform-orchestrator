package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Opt-in actual Terraform/OpenTofu against the owned typed HTTP fixture. These
// commands prove protocol/state behavior, not Core/PostgreSQL retention or IAM.
func TestNativeAgentRunnerActualCLI(t *testing.T) {
	providerBinary := os.Getenv("NATIVE_RUNNER_PROVIDER_BINARY")
	if providerBinary == "" {
		t.Skip("set NATIVE_RUNNER_PROVIDER_BINARY to the exact owned candidate binary")
	}
	require.FileExists(t, providerBinary)
	clearEnv(t)
	for _, name := range []string{"terraform", "tofu"} {
		t.Run(name, func(t *testing.T) {
			binary, err := exec.LookPath(name)
			require.NoError(t, err)
			f := &nativeAgentWireFixture{}
			server := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
			t.Cleanup(server.Close)
			dir := t.TempDir()
			configPath := filepath.Join(dir, "cli.tfrc")
			require.NoError(t, os.WriteFile(configPath, []byte(fmt.Sprintf(`provider_installation { dev_overrides { "stellwerk-labs/platform-orchestrator" = %q } direct {} }`, filepath.Dir(providerBinary))), 0600))
			t.Setenv("TF_CLI_CONFIG_FILE", configPath)
			writeNativeCLIConfig(t, dir, server.URL, `"initial"`, false, false)
			nativeCLI(t, binary, dir, true, "plan", "-out=initial.plan", "-input=false", "-no-color")
			nativeCLI(t, binary, dir, true, "apply", "-input=false", "-no-color", "initial.plan")
			require.Equal(t, nativeTestUUID, nativeCLIState(t, dir, "created")["uuid"])
			// Use the same inner-export expression as the public example, after the
			// initial computed-input Create has separately proved unknown resolution.
			example, err := os.ReadFile(filepath.Join(dir, "main.tf"))
			require.NoError(t, err)
			example = []byte(strings.ReplaceAll(string(example), "native_execution = terraform_data.inventory.output", "native_execution = jsonencode(local.catalogue.native_execution)"))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "main.tf"), example, 0600))
			nativeCLI(t, binary, dir, true, "plan", "-detailed-exitcode", "-input=false", "-no-color")
			writeNativeCLIConfig(t, dir, server.URL, `"changed"`, false, false)
			nativeCLI(t, binary, dir, true, "plan", "-out=description.plan", "-input=false", "-no-color")
			f.mu.Lock()
			delete(nativeFixtureConfiguration(t, f.runner), "native_execution")
			f.mu.Unlock()
			failed := nativeCLI(t, binary, dir, false, "apply", "-input=false", "-no-color", "description.plan")
			require.Contains(t, string(failed), "outside the reviewed plan")
			require.NotContains(t, string(failed), "inconsistent result")
			retained := nativeCLIState(t, dir, "stale-plan-attention")
			require.Equal(t, nativeTestUUID, retained["uuid"])
			f.mu.Lock()
			require.NotContains(t, nativeFixtureConfiguration(t, f.runner), "native_execution")
			require.Len(t, f.patches, 1)
			require.NotContains(t, f.patches[0], "runner_configuration")
			f.mu.Unlock()
			nativeCLI(t, binary, dir, true, "apply", "-refresh-only", "-auto-approve", "-input=false", "-no-color")
			// The next reviewed plan sees null -> inventory and explicitly re-enables it.
			nativeCLI(t, binary, dir, true, "apply", "-auto-approve", "-input=false", "-no-color")
			nativeCLI(t, binary, dir, true, "plan", "-detailed-exitcode", "-input=false", "-no-color")
			writeNativeCLIConfig(t, dir, server.URL, `"changed"`, true, false)
			nativeCLI(t, binary, dir, true, "apply", "-auto-approve", "-input=false", "-no-color")
			nativeCLI(t, binary, dir, true, "plan", "-detailed-exitcode", "-input=false", "-no-color")
			require.Nil(t, nativeFixtureConfiguration(t, nativeCLIState(t, dir, "disabled"))["native_execution"])
			writeNativeCLIConfig(t, dir, server.URL, `""`, false, false)
			nativeCLI(t, binary, dir, true, "apply", "-auto-approve", "-input=false", "-no-color")
			nativeCLI(t, binary, dir, true, "plan", "-detailed-exitcode", "-input=false", "-no-color")
			text, err := os.ReadFile(filepath.Join(dir, "main.tf"))
			require.NoError(t, err)
			text = []byte(strings.ReplaceAll(string(text), "max_parallel_jobs = 2", "max_parallel_jobs = 3"))
			nextKey := strings.Replace(nativeTestKey, "rKpef0=", "rKpeg0=", 1)
			text = []byte(strings.ReplaceAll(string(text), fmt.Sprintf("%q", nativeTestKey), fmt.Sprintf("%q", nextKey)))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "main.tf"), text, 0600))
			nativeCLI(t, binary, dir, true, "apply", "-auto-approve", "-input=false", "-no-color")
			nativeCLI(t, binary, dir, true, "plan", "-detailed-exitcode", "-input=false", "-no-color")
			changed := nativeFixtureConfiguration(t, nativeCLIState(t, dir, "key-capacity-changed"))
			require.Equal(t, float64(3), changed["max_parallel_jobs"])
			require.Equal(t, nextKey, changed["key"])
			writeNativeCLIConfig(t, dir, server.URL, "null", false, false)
			invalid := nativeCLI(t, binary, dir, false, "plan", "-input=false", "-no-color")
			require.Contains(t, string(invalid), "cannot clear an existing description to null")
			writeNativeCLIConfig(t, dir, server.URL, `""`, false, false)
			nativeCLI(t, binary, dir, true, "apply", "-auto-approve", "-input=false", "-no-color")
			destroy := nativeCLI(t, binary, dir, false, "destroy", "-auto-approve", "-input=false", "-no-color")
			require.Contains(t, string(destroy), "registration is retained")
			require.Equal(t, nativeTestUUID, nativeCLIState(t, dir, "protected-destroy")["uuid"])
			// Import uses a separate owned empty state, never state-rm on the tracked proof.
			imported := t.TempDir()
			writeNativeCLIConfig(t, imported, server.URL, `""`, false, false)
			nativeCLI(t, binary, imported, true, "import", "-input=false", "-no-color", "platform-orchestrator_native_agent_runner.test", nativeTestID)
			require.Equal(t, nativeTestUUID, nativeCLIState(t, imported, "imported")["uuid"])
			nativeCLI(t, binary, imported, true, "apply", "-auto-approve", "-input=false", "-no-color")
			nativeCLI(t, binary, imported, true, "plan", "-detailed-exitcode", "-input=false", "-no-color")
			newNull := t.TempDir()
			writeNativeCLIConfig(t, newNull, server.URL, `"initial"`, true, false)
			nullPlan := nativeCLI(t, binary, newNull, false, "plan", "-input=false", "-no-color")
			require.Contains(t, string(nullPlan), "requires non-null approved inventory")
			unknownInvalid := t.TempDir()
			writeNativeCLIConfig(t, unknownInvalid, server.URL, `"initial"`, false, true)
			nativeCLI(t, binary, unknownInvalid, true, "plan", "-out=unknown.plan", "-input=false", "-no-color")
			f.mu.Lock()
			before := f.creates
			f.mu.Unlock()
			invalid = nativeCLI(t, binary, unknownInvalid, false, "apply", "-input=false", "-no-color", "unknown.plan")
			require.Contains(t, string(invalid), "public v2 catalogue")
			f.mu.Lock()
			require.Equal(t, before, f.creates)
			f.mu.Unlock()
		})
	}
}

func nativeCLI(t *testing.T, binary, dir string, success bool, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "TF_LOG=", "TF_LOG_PATH=")
	output, err := command.CombinedOutput()
	if success {
		require.NoError(t, err, "%s %v: %s", filepath.Base(binary), args, output)
	} else {
		require.Error(t, err, "expected refusal: %s", output)
	}
	return output
}
func writeNativeCLIConfig(t *testing.T, dir, url, description string, disabled, invalid bool) {
	t.Helper()
	inventory := nativeTestInventory
	if invalid {
		inventory = `{"tools":[]}`
	}
	export, err := json.Marshal(map[string]any{"supported_protocols": []string{"stellwerk.pipeline/v2", "stellwerk.pipeline.plan-apply/v2"}, "capabilities": []string{}, "native_execution": json.RawMessage(inventory)})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-catalogue.json"), export, 0600))
	inventoryExpression := "terraform_data.inventory.output"
	if disabled {
		inventoryExpression = "null"
	}
	text := fmt.Sprintf(`terraform {
 required_providers {
  platform-orchestrator = { source = "stellwerk-labs/platform-orchestrator" }
 }
}
provider "platform-orchestrator" {
 api_url = %q
 org_id = %q
 auth_token = "owned-fixture-token"
 octl_config_file = ""
}
locals {
 catalogue = jsondecode(file("${path.module}/rollout-catalogue.json"))
}
resource "terraform_data" "inventory" { input = jsonencode(local.catalogue.native_execution) }
resource "platform-orchestrator_native_agent_runner" "test" {
 id = %q
 description = %s
 runner_configuration = {
  key = %q
  max_parallel_jobs = 2
  supported_protocols = local.catalogue.supported_protocols
  capabilities = local.catalogue.capabilities
  native_execution = %s
 }
 state_storage_configuration = { type = "s3", s3_configuration = {bucket = "native-state"} }
}
data "platform-orchestrator_native_agent_runner" "test" { id = platform-orchestrator_native_agent_runner.test.id }
output "runner_uuid" { value = data.platform-orchestrator_native_agent_runner.test.uuid }
`, url, nativeTestOrg, nativeTestID, description, nativeTestKey, inventoryExpression)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.tf"), []byte(text), 0600))
}
func nativeCLIState(t *testing.T, dir, label string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "terraform.tfstate"))
	require.NoError(t, err)
	if destination := os.Getenv("NATIVE_RUNNER_EVIDENCE_DIR"); destination != "" {
		name := strings.ReplaceAll(t.Name(), "/", "-") + "-" + label + ".tfstate"
		require.NoError(t, os.WriteFile(filepath.Join(destination, name), raw, 0600))
	}
	var state struct {
		Resources []struct {
			Mode, Type string
			Instances  []struct{ Attributes map[string]any }
		}
	}
	require.NoError(t, json.Unmarshal(raw, &state))
	for _, r := range state.Resources {
		if r.Mode == "managed" && r.Type == "platform-orchestrator_native_agent_runner" {
			require.Len(t, r.Instances, 1)
			return r.Instances[0].Attributes
		}
	}
	t.Fatal("native managed state disappeared")
	return nil
}
