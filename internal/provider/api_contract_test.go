package provider

import (
	"crypto/sha1" //nolint:gosec // Git object IDs are SHA-1 by definition.
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	rollouts "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/progressive-rollouts"
)

const (
	coreSpecPath                = "../clients/platform-orchestrator-cp/spec.yaml"
	progressiveRolloutsSpecPath = "../clients/progressive-rollouts/spec.yaml"
)

// Raw maps instead of kin-openapi types: the checks below are about the exact
// vendored bytes and reference graph, not about a loader's interpretation.
func loadContractDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &document))
	return document
}

func contractObject(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	require.True(t, ok, "contract value must be an object, got %T", value)
	return result
}

func contractReferences(value any, found map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		if reference, ok := typed["$ref"].(string); ok {
			found[reference] = true
		}
		for _, child := range typed {
			contractReferences(child, found)
		}
	case []any:
		for _, child := range typed {
			contractReferences(child, found)
		}
	}
}

// recordedSourceValue returns the single "- <label>: `<value>`" entry of a
// vendored server contract's SOURCE.md.
func recordedSourceValue(t *testing.T, sourcePath, label string) string {
	t.Helper()
	raw, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	matches := regexp.MustCompile("(?m)^- "+regexp.QuoteMeta(label)+": `([^`]+)`$").FindAllSubmatch(raw, -1)
	require.Len(t, matches, 1, "%s must record exactly one %s", sourcePath, label)
	return string(matches[0][1])
}

// The vendored server documents are the exact bytes their SOURCE.md pins;
// scripts/check-api-specs.sh compares the same pins with the upstream files.
func TestServerContractsMatchRecordedSource(t *testing.T) {
	t.Parallel()

	for directory, repository := range map[string]string{
		"../clients/platform-orchestrator-cp": "stellwerk-labs/platform-orchestrator-cp",
		"../clients/platform-orchestrator-dp": "stellwerk-labs/platform-orchestrator-dp",
	} {
		source := directory + "/SOURCE.md"
		raw, err := os.ReadFile(directory + "/spec.yaml")
		require.NoError(t, err)
		require.Equal(t, recordedSourceValue(t, source, "File SHA-256"), fmt.Sprintf("%x", sha256.Sum256(raw)),
			"%s/spec.yaml must be the byte-identical document recorded in SOURCE.md", directory)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(raw))), raw...))
		require.Equal(t, recordedSourceValue(t, source, "Git blob"), fmt.Sprintf("%x", blob), "Git blob in %s", source)
		require.Regexp(t, "^[0-9a-f]{40}$", recordedSourceValue(t, source, "Commit"))
		require.Equal(t, repository, recordedSourceValue(t, source, "Repository"))
		require.Equal(t, "openapi/spec.yaml", recordedSourceValue(t, source, "File"))
	}
}

// The vendored Core document stays byte-exact to its pinned source. A failure
// here is a source defect to fix in Core, never a reason to relax the check.
func TestCoreOpenAPISpecValidatesWithExamples(t *testing.T) {
	t.Parallel()

	document, err := openapi3.NewLoader().LoadFromFile(coreSpecPath)
	require.NoError(t, err)
	require.NoError(t, document.Validate(t.Context(), openapi3.EnableExamplesValidation()))
}

// Core exposes plugins only through neutral discovery and invocation. Plugin
// routes and models belong to the plugin's own contract.
func TestCoreContractContainsNoProgressiveRolloutsSurface(t *testing.T) {
	t.Parallel()

	document := loadContractDocument(t, coreSpecPath)
	for route := range contractObject(t, document["paths"]) {
		require.False(t, route == "/orgs/{orgId}/rollouts" || strings.HasPrefix(route, "/orgs/{orgId}/rollouts/"), "Core route %s belongs to the plugin contract", route)
	}
	for name := range contractObject(t, contractObject(t, document["components"])["schemas"]) {
		require.False(t, strings.HasPrefix(name, "ProgressiveRollout"), "Core schema %s belongs to the plugin contract", name)
	}
	operations := map[string]bool{}
	for _, item := range contractObject(t, document["paths"]) {
		for _, value := range contractObject(t, item) {
			if operation, ok := value.(map[string]any); ok {
				if id, ok := operation["operationId"].(string); ok {
					operations[id] = true
				}
			}
		}
	}
	for _, id := range []string{"getPluginCapability", "listPluginContributions", "invokePluginOperation"} {
		require.True(t, operations[id], "Core must provide plugin %s", id)
	}
}

// progressiveRolloutsContractOperations returns the plugin operations by ID.
// The contract has no routes: every operation is an OpenAPI Operation Object in
// x-stellwerk-operations, invoked only through Core plugin invocation.
func progressiveRolloutsContractOperations(t *testing.T, document map[string]any) map[string]map[string]any {
	t.Helper()
	entries, ok := document["x-stellwerk-operations"].([]any)
	require.True(t, ok, "x-stellwerk-operations must be a list")
	operations := map[string]map[string]any{}
	for _, entry := range entries {
		operation := contractObject(t, entry)
		id, ok := operation["operationId"].(string)
		require.True(t, ok, "operation without operationId: %v", operation)
		require.NotContains(t, operations, id, "duplicate operationId")
		operations[id] = operation
	}
	return operations
}

// contractPathParameters resolves the path parameter names of one operation.
func contractPathParameters(t *testing.T, document, operation map[string]any) []string {
	t.Helper()
	shared := contractObject(t, contractObject(t, document["components"])["parameters"])
	var names []string
	parameters, _ := operation["parameters"].([]any)
	for _, raw := range parameters {
		parameter := contractObject(t, raw)
		if reference, ok := parameter["$ref"].(string); ok {
			parameter = contractObject(t, shared[strings.TrimPrefix(reference, "#/components/parameters/")])
		}
		if parameter["in"] == "path" {
			name, ok := parameter["name"].(string)
			require.True(t, ok)
			names = append(names, name)
		}
	}
	return names
}

func TestProgressiveRolloutsAPIContractIsPinnedAndValid(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(progressiveRolloutsSpecPath)
	require.NoError(t, err)
	require.Equal(t, "bc1e49c348fc5c95a2c5eef6d5fd2f0bfb5a35ccbc374c3bab7a6fb76801a0fd", fmt.Sprintf("%x", sha256.Sum256(raw)),
		"spec.yaml changed: vendor the canonical plugin contract byte-identically and update SOURCE.md")

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	document, err := loader.LoadFromData(raw)
	require.NoError(t, err)
	require.NoError(t, document.Validate(t.Context(), openapi3.EnableExamplesValidation()))
	require.Equal(t, "v1alpha", document.Info.Version)
	require.Zero(t, document.Paths.Len(), "the plugin publishes no client routes")

	// Validate every operation as a strict Operation Object by mounting it on a
	// synthetic route that carries exactly its path parameters.
	var contract map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &contract))
	paths := map[string]any{}
	for id, operation := range progressiveRolloutsContractOperations(t, contract) {
		route := "/" + id
		for _, name := range contractPathParameters(t, contract, operation) {
			route += "/{" + name + "}"
		}
		method, ok := operation["x-method"].(string)
		require.True(t, ok, id)
		paths[route] = map[string]any{strings.ToLower(method): operation}
	}
	contract["paths"] = paths
	encoded, err := json.Marshal(contract)
	require.NoError(t, err)
	mounted, err := loader.LoadFromData(encoded)
	require.NoError(t, err)
	require.NoError(t, mounted.Validate(t.Context(), openapi3.EnableExamplesValidation()))
}

func TestProgressiveRolloutsAPIContractIsSelfContainedPluginSurface(t *testing.T) {
	t.Parallel()

	document := loadContractDocument(t, progressiveRolloutsSpecPath)
	require.Equal(t, "dev.stellwerk.progressive-rollouts", document["x-stellwerk-plugin-id"])
	require.Equal(t, "Progressive Rollouts", document["x-stellwerk-plugin-display-name"])
	require.Empty(t, contractObject(t, document["paths"]))

	operations := progressiveRolloutsContractOperations(t, document)
	require.Len(t, operations, 15)
	for id, operation := range operations {
		require.Contains(t, []any{"GET", "POST", "PATCH", "DELETE"}, operation["x-method"], id)
		require.IsType(t, true, operation["x-mutating"], id)
		require.NotEmpty(t, operation["x-required-feature"], id)
		require.NotContains(t, contractPathParameters(t, document, operation), "orgId", "%s: the organization comes from the Core route", id)
	}

	// Components are exactly the transitive closure of the operations.
	references := map[string]bool{}
	contractReferences(document["x-stellwerk-operations"], references)
	components := contractObject(t, document["components"])
	for {
		before := len(references)
		for reference := range references {
			require.True(t, strings.HasPrefix(reference, "#/components/"), "non-local reference %s", reference)
			parts := strings.Split(strings.TrimPrefix(reference, "#/components/"), "/")
			require.Len(t, parts, 2, reference)
			category := contractObject(t, components[parts[0]])
			require.Contains(t, category, parts[1], "unresolved reference %s", reference)
			contractReferences(category[parts[1]], references)
		}
		if len(references) == before {
			break
		}
	}
	for category, values := range components {
		for name := range contractObject(t, values) {
			require.True(t, references["#/components/"+category+"/"+name], "orphan component %s/%s", category, name)
		}
	}
}

// Rollouts snapshots carry two Core value sets. The contract mirrors them so it
// stays self-contained; Core remains authoritative and the copies must match.
func TestProgressiveRolloutsContractMirrorsCoreValueSets(t *testing.T) {
	t.Parallel()

	plugin := contractObject(t, contractObject(t, loadContractDocument(t, progressiveRolloutsSpecPath)["components"])["schemas"])
	core := contractObject(t, contractObject(t, loadContractDocument(t, coreSpecPath)["components"])["schemas"])
	var mirrors []string
	for name, raw := range plugin {
		schema := contractObject(t, raw)
		coreName, mirrored := schema["x-stellwerk-core-schema"]
		if !mirrored {
			require.True(t, strings.HasPrefix(name, "ProgressiveRollout"), "schema %s is neither plugin-owned nor a marked Core mirror", name)
			continue
		}
		require.Equal(t, name, coreName)
		mirror := maps.Clone(schema)
		delete(mirror, "x-stellwerk-core-schema")
		delete(mirror, "description")
		require.Equal(t, core[name], mirror, "mirror %s must equal the Core definition", name)
		mirrors = append(mirrors, name)
	}
	require.ElementsMatch(t, []string{"ModuleVersionPinStatus", "ModuleVersionSemanticStatus"}, mirrors)
}

func TestRolloutExecutionResponseFieldsRoundTrip(t *testing.T) {
	t.Parallel()

	const response = `{"executions":[{"environment_id":"11111111-1111-4111-8111-111111111111","project_id":"project","wave_id":"wave","state":"Failed","attempts":1,"pre_deployment_attempts":0,"changed":true,"actual_versions":{},"resource_version":2,"effective_base_deployment_id":"22222222-2222-4222-8222-222222222222","first_forward_deployment_id":"33333333-3333-4333-8333-333333333333","admission_base_deployment_id":"44444444-4444-4444-8444-444444444444","rollback_available":true,"rollback_retry_available":false,"rollback_retry_pending":false}]}`
	var detail rollouts.ProgressiveRolloutDetail
	require.NoError(t, json.Unmarshal([]byte(response), &detail))
	require.Len(t, detail.Executions, 1)
	execution := detail.Executions[0]
	require.NotNil(t, execution.FirstForwardDeploymentId)
	require.Equal(t, "33333333-3333-4333-8333-333333333333", execution.FirstForwardDeploymentId.String())
	require.NotNil(t, execution.AdmissionBaseDeploymentId)
	require.Equal(t, "44444444-4444-4444-8444-444444444444", execution.AdmissionBaseDeploymentId.String())
	require.NotNil(t, execution.RollbackAvailable)
	require.True(t, *execution.RollbackAvailable)
	require.NotNil(t, execution.RollbackRetryAvailable)
	require.False(t, *execution.RollbackRetryAvailable)
	require.NotNil(t, execution.RollbackRetryPending)
	require.False(t, *execution.RollbackRetryPending)

	encoded, err := json.Marshal(detail)
	require.NoError(t, err)
	var roundTrip map[string]any
	require.NoError(t, json.Unmarshal(encoded, &roundTrip))
	executions, ok := roundTrip["executions"].([]any)
	require.True(t, ok)
	require.Len(t, executions, 1)
	roundTripExecution, ok := executions[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "33333333-3333-4333-8333-333333333333", roundTripExecution["first_forward_deployment_id"])
	require.Equal(t, "44444444-4444-4444-8444-444444444444", roundTripExecution["admission_base_deployment_id"])
	require.Equal(t, true, roundTripExecution["rollback_available"])
	require.Equal(t, false, roundTripExecution["rollback_retry_available"])
	require.Equal(t, false, roundTripExecution["rollback_retry_pending"])
}
