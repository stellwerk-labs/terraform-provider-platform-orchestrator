package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/stretchr/testify/require"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
	rollouts "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/progressive-rollouts"
)

func newRolloutsTestClient(t *testing.T, server *httptest.Server) progressiveRolloutsClient {
	t.Helper()
	client, err := cp.NewClientWithResponses(server.URL, cp.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+rolloutsFixtureToken)
		return nil
	}))
	require.NoError(t, err)
	return progressiveRolloutsClient{cp: client, orgID: rolloutsFixtureOrg}
}

func requireRolloutsDiagnostic(t *testing.T, diagnostics diag.Diagnostics, code string) {
	t.Helper()
	require.True(t, diagnostics.HasError(), "expected %s", code)
	detail := diagnostics.Errors()[0].Detail()
	require.True(t, strings.HasPrefix(detail, code+": "), "diagnostic %q must start with the stable code %s", detail, code)
	require.Contains(t, detail, progressiveRolloutsPluginDisplayName)
	require.Contains(t, detail, progressiveRolloutsPluginID)
	require.Equal(t, progressiveRolloutsUnavailableTitle, diagnostics.Errors()[0].Summary())
}

func TestProgressiveRolloutsAvailabilityStatesProduceStableCodes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		state, code string
	}{
		{state: "not_installed", code: "plugin_not_installed"},
		{state: "not_enabled", code: "plugin_not_enabled"},
		{state: "not_entitled", code: "plugin_not_entitled"},
		{state: "incompatible", code: "plugin_incompatible"},
		{state: "unavailable", code: "plugin_unavailable"},
		{state: "a_future_state", code: "plugin_unavailable"},
	} {
		t.Run(test.state, func(t *testing.T) {
			t.Parallel()
			fixture, server := newRolloutsCoreFixture(t)
			fixture.configure(func(f *rolloutsCoreFixture) {
				f.state, f.listed, f.adminAction = test.state, test.state != "not_installed", "Contact the platform team."
			})
			var diagnostics diag.Diagnostics
			require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "createProgressiveRollout"))
			requireRolloutsDiagnostic(t, diagnostics, test.code)
			require.Contains(t, diagnostics.Errors()[0].Detail(), "Contact the platform team.", "Core's administrator action must reach the user")
			require.Empty(t, fixture.operationsInvoked(), "nothing may be invoked after a refused discovery")
		})
	}
}

func TestProgressiveRolloutsServerWithoutDiscoveryIsNotInstalled(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name                            string
		capabilityStatus, contributions int
		code, detail                    string
	}{
		{name: "capability route missing", capabilityStatus: http.StatusNotFound, contributions: http.StatusOK, code: "plugin_not_installed", detail: "predates plugin capability discovery"},
		{name: "capability not implemented", capabilityStatus: http.StatusNotImplemented, contributions: http.StatusOK, code: "plugin_not_installed", detail: "predates plugin capability discovery"},
		{name: "contribution route missing", capabilityStatus: http.StatusOK, contributions: http.StatusNotFound, code: "plugin_not_installed", detail: "predates plugin capability discovery"},
		{name: "discovery unavailable", capabilityStatus: http.StatusServiceUnavailable, contributions: http.StatusOK, code: "plugin_unavailable", detail: "cannot be discovered"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture, server := newRolloutsCoreFixture(t)
			fixture.configure(func(f *rolloutsCoreFixture) {
				f.capabilityStatus, f.contributionsStatus = test.capabilityStatus, test.contributions
			})
			var diagnostics diag.Diagnostics
			require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "getProgressiveRollout"))
			requireRolloutsDiagnostic(t, diagnostics, test.code)
			require.Contains(t, diagnostics.Errors()[0].Detail(), test.detail)
		})
	}
}

func TestProgressiveRolloutsDiscoveryPermissionErrorIsNotAPluginState(t *testing.T) {
	t.Parallel()

	fixture, server := newRolloutsCoreFixture(t)
	fixture.configure(func(f *rolloutsCoreFixture) { f.capabilityStatus = http.StatusForbidden })
	var diagnostics diag.Diagnostics
	require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "getProgressiveRollout"))
	require.Equal(t, PO_API_ERR, diagnostics.Errors()[0].Summary())
	require.Contains(t, diagnostics.Errors()[0].Detail(), "HTTP 403")
	require.NotContains(t, diagnostics.Errors()[0].Detail(), "plugin_")
}

func TestProgressiveRolloutsLicenceExpiryKeepsReadsAndSafetyCommands(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"not_entitled", "not_enabled"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			fixture, server := newRolloutsCoreFixture(t)
			if state == "not_entitled" {
				fixture.expireLicence()
			} else {
				// An organization without enablement has no licence projection.
				fixture.configure(func(f *rolloutsCoreFixture) { f.state, f.features = state, nil })
			}
			client := newRolloutsTestClient(t, server)
			code := "plugin_" + state
			for _, operationID := range []string{"getProgressiveRollout", "previewProgressiveRollout", "commandProgressiveRollout"} {
				var diagnostics diag.Diagnostics
				require.True(t, client.require(t.Context(), &diagnostics, operationID), "%s must stay available: %v", operationID, diagnostics)
			}
			for _, operationID := range []string{"createProgressiveRollout", "updateProgressiveRolloutDraft", "prepareProgressiveRollout"} {
				var diagnostics diag.Diagnostics
				require.False(t, client.require(t.Context(), &diagnostics, operationID), operationID)
				requireRolloutsDiagnostic(t, diagnostics, code)
			}
		})
	}
}

func TestProgressiveRolloutsHiddenLicenceBlocksReads(t *testing.T) {
	t.Parallel()

	fixture, server := newRolloutsCoreFixture(t)
	fixture.expireLicence()
	fixture.configure(func(f *rolloutsCoreFixture) {
		f.features = map[string]rolloutsFeatureAccess{rolloutsFixtureFeature: {}}
	})
	var diagnostics diag.Diagnostics
	require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "getProgressiveRollout"))
	requireRolloutsDiagnostic(t, diagnostics, "plugin_not_entitled")
}

func TestProgressiveRolloutsOperationMustBeAdvertisedExactly(t *testing.T) {
	t.Parallel()

	t.Run("method mismatch", func(t *testing.T) {
		t.Parallel()
		fixture, server := newRolloutsCoreFixture(t)
		fixture.configure(func(f *rolloutsCoreFixture) { f.operation("createProgressiveRollout").method = http.MethodPut })
		var diagnostics diag.Diagnostics
		require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "createProgressiveRollout"))
		requireRolloutsDiagnostic(t, diagnostics, "plugin_incompatible")
	})
	t.Run("operation missing from installed version", func(t *testing.T) {
		t.Parallel()
		fixture, server := newRolloutsCoreFixture(t)
		fixture.configure(func(f *rolloutsCoreFixture) { f.withdraw("createProgressiveRollout") })
		var diagnostics diag.Diagnostics
		require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "createProgressiveRollout"))
		requireRolloutsDiagnostic(t, diagnostics, "plugin_incompatible")
	})
	t.Run("operation withheld by a partial licence", func(t *testing.T) {
		t.Parallel()
		fixture, server := newRolloutsCoreFixture(t)
		fixture.configure(func(f *rolloutsCoreFixture) {
			f.withdraw("createProgressiveRollout")
			f.features["another-feature"] = rolloutsFeatureAccess{Read: true}
		})
		var diagnostics diag.Diagnostics
		require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "createProgressiveRollout"))
		requireRolloutsDiagnostic(t, diagnostics, "plugin_not_entitled")
	})
	t.Run("installed but not routed", func(t *testing.T) {
		t.Parallel()
		fixture, server := newRolloutsCoreFixture(t)
		fixture.configure(func(f *rolloutsCoreFixture) { f.listed = false })
		var diagnostics diag.Diagnostics
		require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "getProgressiveRollout"))
		requireRolloutsDiagnostic(t, diagnostics, "plugin_unavailable")
	})
	t.Run("every requested operation is checked", func(t *testing.T) {
		t.Parallel()
		fixture, server := newRolloutsCoreFixture(t)
		fixture.expireLicence()
		var diagnostics diag.Diagnostics
		require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "previewProgressiveRollout", "prepareProgressiveRollout"))
		requireRolloutsDiagnostic(t, diagnostics, "plugin_not_entitled")
	})
	t.Run("provider refuses foreign operations", func(t *testing.T) {
		t.Parallel()
		_, server := newRolloutsCoreFixture(t)
		var diagnostics diag.Diagnostics
		require.False(t, newRolloutsTestClient(t, server).require(t.Context(), &diagnostics, "archiveEverything"))
		require.Equal(t, PO_PROVIDER_ERR, diagnostics.Errors()[0].Summary())
	})
}

func TestProgressiveRolloutsInvocationUsesBoundedEnvelope(t *testing.T) {
	t.Parallel()

	received := make(chan cp.PluginInvocation, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/orgs/demo/plugins/"+progressiveRolloutsPluginID+"/invoke" {
			http.NotFound(w, request)
			return
		}
		var invocation cp.PluginInvocation
		if err := json.NewDecoder(request.Body).Decode(&invocation); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- invocation
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"body":{"id":"11111111-1111-4111-8111-111111111111","state":"Draft","resource_version":5}}`))
	}))
	t.Cleanup(server.Close)
	client, err := cp.NewClientWithResponses(server.URL)
	require.NoError(t, err)

	var diagnostics diag.Diagnostics
	result, ok := invokeProgressiveRollouts[rollouts.ProgressiveRollout](t.Context(), progressiveRolloutsClient{cp: client, orgID: "demo"}, progressiveRolloutsCall{
		action: "update Progressive Rollout Draft", operationID: "updateProgressiveRolloutDraft",
		pathParameters: map[string]string{"rolloutId": "11111111-1111-4111-8111-111111111111"},
		body:           map[string]int64{"expected_resource_version": 4}, idempotencyKey: "stable-key", expectedStatus: http.StatusOK,
	}, &diagnostics)
	require.True(t, ok, diagnostics)
	require.Equal(t, rollouts.ProgressiveRolloutStateDraft, result.State)
	require.Equal(t, int64(5), result.ResourceVersion)
	invocation := <-received
	require.Equal(t, "updateProgressiveRolloutDraft", invocation.OperationId)
	require.Equal(t, "stable-key", *invocation.IdempotencyKey)
	require.Equal(t, map[string]string{"rolloutId": "11111111-1111-4111-8111-111111111111"}, *invocation.PathParameters)
	require.JSONEq(t, `{"expected_resource_version":4}`, string(*invocation.Body))
	require.Nil(t, invocation.Query)
}

func TestProgressiveRolloutsInvocationReportsGatewayAndPluginRefusals(t *testing.T) {
	t.Parallel()

	fixture, server := newRolloutsCoreFixture(t)
	client := newRolloutsTestClient(t, server)
	fixture.expireLicence()

	var diagnostics diag.Diagnostics
	_, ok := invokeProgressiveRollouts[rollouts.ProgressiveRolloutDetail](t.Context(), client, progressiveRolloutsCall{
		action: "control Progressive Rollout", operationID: "commandProgressiveRollout",
		pathParameters: map[string]string{"rolloutId": "11111111-1111-4111-8111-111111111111", "rolloutCommand": "start"},
		body:           map[string]int64{"expected_resource_version": 1}, idempotencyKey: "start-key", expectedStatus: http.StatusOK,
	}, &diagnostics)
	require.False(t, ok)
	require.Equal(t, "Required Stellwerk plugin unavailable", diagnostics.Errors()[0].Summary())
	require.Contains(t, diagnostics.Errors()[0].Detail(), "HTTP 409 (plugin_not_entitled)")

	diagnostics = nil
	_, ok = invokeProgressiveRollouts[rollouts.ProgressiveRolloutDetail](t.Context(), client, progressiveRolloutsCall{
		action: "read Progressive Rollout", operationID: "getProgressiveRollout",
		pathParameters: map[string]string{"rolloutId": "11111111-1111-4111-8111-111111111111"}, expectedStatus: http.StatusOK,
	}, &diagnostics)
	require.False(t, ok)
	require.Equal(t, PO_API_ERR, diagnostics.Errors()[0].Summary())
	require.Contains(t, diagnostics.Errors()[0].Detail(), "read Progressive Rollout failed with plugin HTTP 404 (not_found): Rollout not found.")
}

func TestProgressiveRolloutsPluginErrorKeepsItsStableCode(t *testing.T) {
	t.Parallel()

	var diagnostics diag.Diagnostics
	addProgressiveRolloutsPluginError(&diagnostics, "create Progressive Rollout", http.StatusForbidden, []byte(`{"code":"plugin_not_entitled"}`))
	require.Equal(t, progressiveRolloutsUnavailableTitle, diagnostics.Errors()[0].Summary())
	require.Equal(t, "create Progressive Rollout failed with plugin HTTP 403 (plugin_not_entitled): plugin_not_entitled", diagnostics.Errors()[0].Detail())

	diagnostics = nil
	addProgressiveRolloutsPluginError(&diagnostics, "create Progressive Rollout", http.StatusBadGateway, []byte(`upstream exploded`))
	require.Equal(t, PO_API_ERR, diagnostics.Errors()[0].Summary())
	require.Contains(t, diagnostics.Errors()[0].Detail(), "upstream exploded")
}

func TestProgressiveRolloutsInvocationRejectsUnknownOperationAndMissingKey(t *testing.T) {
	t.Parallel()

	var diagnostics diag.Diagnostics
	_, ok := invokeProgressiveRollouts[struct{}](t.Context(), progressiveRolloutsClient{}, progressiveRolloutsCall{action: "call", operationID: "arbitraryURL"}, &diagnostics)
	require.False(t, ok)
	require.Contains(t, diagnostics.Errors()[0].Detail(), "does not permit")

	diagnostics = nil
	_, ok = invokeProgressiveRollouts[struct{}](t.Context(), progressiveRolloutsClient{}, progressiveRolloutsCall{action: "control", operationID: "commandProgressiveRollout", body: struct{}{}}, &diagnostics)
	require.False(t, ok)
	require.Contains(t, diagnostics.Errors()[0].Detail(), "idempotency key is required")
}

func TestProgressiveRolloutsOperationCatalogueMatchesPluginContract(t *testing.T) {
	t.Parallel()

	require.Equal(t, map[string]progressiveRolloutsOperation{
		"getProgressiveRollout":         {method: http.MethodGet},
		"previewProgressiveRollout":     {method: http.MethodPost},
		"createProgressiveRollout":      {method: http.MethodPost, mutating: true},
		"updateProgressiveRolloutDraft": {method: http.MethodPatch, mutating: true},
		"prepareProgressiveRollout":     {method: http.MethodPost, mutating: true},
		"commandProgressiveRollout":     {method: http.MethodPost, mutating: true},
	}, progressiveRolloutsOperations)

	document := loadContractDocument(t, progressiveRolloutsSpecPath)
	contract := progressiveRolloutsContractOperations(t, document)
	pathParameters := map[string][]string{
		"getProgressiveRollout":         {"rolloutId"},
		"previewProgressiveRollout":     nil,
		"createProgressiveRollout":      nil,
		"updateProgressiveRolloutDraft": {"rolloutId"},
		"prepareProgressiveRollout":     {"rolloutId"},
		"commandProgressiveRollout":     {"rolloutId", "rolloutCommand"},
	}
	for id, operation := range progressiveRolloutsOperations {
		published, found := contract[id]
		require.True(t, found, "%s is not in the plugin contract", id)
		require.Equal(t, operation.method, published["x-method"], id)
		require.Equal(t, operation.mutating, published["x-mutating"], id)
		require.ElementsMatch(t, pathParameters[id], contractPathParameters(t, document, published), id)
	}

	// Every command the provider sends must be a declared rolloutCommand value.
	var commands []any
	parameters, ok := contract["commandProgressiveRollout"]["parameters"].([]any)
	require.True(t, ok)
	for _, raw := range parameters {
		if parameter := contractObject(t, raw); parameter["name"] == "rolloutCommand" {
			commands, ok = contractObject(t, parameter["schema"])["enum"].([]any)
			require.True(t, ok)
		}
	}
	for _, command := range []rolloutCommand{rolloutCommandStart, rolloutCommandReturnToDraft, rolloutCommandPause, rolloutCommandResume, rolloutCommandCancel, rolloutCommandCancelAndRollback, rolloutCommandRollback} {
		require.Contains(t, commands, string(command))
	}
}

func TestProgressiveRolloutsGraceWarningIsOneWarningDiagnostic(t *testing.T) {
	t.Parallel()

	fixture, server := newRolloutsCoreFixture(t)
	fixture.enterGrace("2026-11-01T00:00:00Z")
	client := newRolloutsTestClient(t, server)
	var diagnostics diag.Diagnostics
	require.True(t, client.require(t.Context(), &diagnostics, "createProgressiveRollout"))
	_, ok := invokeProgressiveRollouts[rollouts.ProgressiveRollout](t.Context(), client, progressiveRolloutsCall{
		action: "create Progressive Rollout", operationID: "createProgressiveRollout",
		body: json.RawMessage(`{"name":"payments"}`), idempotencyKey: "grace", expectedStatus: http.StatusCreated,
	}, &diagnostics)
	require.True(t, ok, "forward work succeeds during grace: %v", diagnostics)
	require.False(t, diagnostics.HasError())
	require.Len(t, diagnostics.Warnings(), 1, "capability, contributions and invoke repeat the warning; it is shown once")
	warning := diagnostics.Warnings()[0]
	require.Equal(t, pluginGraceWarningSummary, warning.Summary())
	require.Equal(t, "The signed plugin licence is in its grace period until 2026-11-01T00:00:00Z. Forward work continues until then; ask an administrator to renew the licence. "+
		"(plugin_entitlement_grace, grace until 2026-11-01T00:00:00Z, features: progressive-rollouts)", warning.Detail())

	fixture.configure(func(f *rolloutsCoreFixture) { f.graceUntil = "" })
	diagnostics = nil
	require.True(t, client.require(t.Context(), &diagnostics, "createProgressiveRollout"))
	require.Empty(t, diagnostics, "a valid licence adds no warning")
}
