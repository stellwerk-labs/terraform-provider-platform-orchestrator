package provider

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/stretchr/testify/require"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/ref"
)

func newPluginEnablementTestResource(t *testing.T) (*pluginManagerFixture, *PluginEnablementResource) {
	t.Helper()
	fixture, server := newPluginManagerFixture(t)
	client, err := cp.NewClientWithResponses(server.URL, cp.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+pluginFixtureToken)
		return nil
	}))
	require.NoError(t, err)
	return fixture, &PluginEnablementResource{cpClient: client, orgId: pluginFixtureOrg}
}

func TestPluginManagerRefusalsAreDiagnosticsWithTheirCode(t *testing.T) {
	t.Parallel()
	for code, summary := range map[string]string{
		"plugin_installation_state":        "Plugin installation not active",
		"plugin_organization_limit":        "Plugin organization limit reached",
		"plugin_work_unsettled":            "Plugin work unsettled",
		"plugin_manager_revision_conflict": "Plugin Manager revision conflict",
		"plugin_installation_not_found":    "Plugin installation not found",
		"plugin_host_disabled":             "Plugin host disabled",
		"plugin_manager_forbidden":         "Plugin Manager forbidden",
		"plugin_enablement_invalid":        PO_API_ERR,
	} {
		var diagnostics diag.Diagnostics
		addPluginManagerError(&diagnostics, "Enable", http.StatusConflict, []byte(`{"error":"`+code+`","message":"refused"}`), nil)
		require.Len(t, diagnostics.Errors(), 1, code)
		require.Equal(t, summary, diagnostics.Errors()[0].Summary(), code)
		require.Contains(t, diagnostics.Errors()[0].Detail(), "Enable failed with HTTP 409 ("+code+"): refused", code)
	}

	var diagnostics diag.Diagnostics
	addPluginManagerError(&diagnostics, "Enable", http.StatusBadGateway, []byte("upstream down"), nil)
	require.Equal(t, PO_API_ERR, diagnostics.Errors()[0].Summary())
	require.Equal(t, "Enable failed with HTTP 502: upstream down", diagnostics.Errors()[0].Detail())
}

// A refused enablement says what the installation's state needs next, with
// the reason its last activation failed.
func TestPluginInstallationStateAdviceNamesTheNextStep(t *testing.T) {
	t.Parallel()
	installation := cp.PluginInstallation{InstallationId: uuid.MustParse(pluginFixtureInstallation), PluginId: pluginFixturePlugin, State: cp.PluginInstallationStateInstalled,
		Diagnostic: &cp.PluginInstallationDiagnostic{Code: cp.PluginDiagnosticPackageDigestMismatch, Message: "The running plugin verified another package."}}
	require.Equal(t, " Installation "+pluginFixtureInstallation+" of plugin dev.stellwerk.progressive-rollouts is installed. Once the plugin runtime serves it, activate it with the Plugin Manager (octl plugin-manager activate "+
		pluginFixtureInstallation+"), then apply again. Its last activation was refused (plugin_package_digest_mismatch): The running plugin verified another package.", installationStateAdvice(&installation))

	installation.State, installation.Diagnostic = cp.PluginInstallationStateUninstalled, nil
	require.Contains(t, installationStateAdvice(&installation), "is uninstalled. Reinstall and activate it with the Plugin Manager (octl plugin-manager reinstall "+pluginFixtureInstallation)
	installation.State = cp.PluginInstallationStatePurged
	require.Contains(t, installationStateAdvice(&installation), "is purged. A purged installation never serves again: install the plugin anew")
	require.Empty(t, installationStateAdvice(nil))
	require.Contains(t, organizationLimitAdvice(nil), "limits how many organizations the installation may enable")
}

// A refused enablement says how many organizations the installation has and
// the limit of its installation-wide grant, and what raises the room.
func TestOrganizationLimitAdviceNamesTheLimitAndTheRemedy(t *testing.T) {
	t.Parallel()
	installation := cp.PluginInstallation{InstallationId: uuid.MustParse(pluginFixtureInstallation), PluginId: pluginFixturePlugin, State: cp.PluginInstallationStateActive,
		Entitlement: &cp.PluginInstallationEntitlement{EnabledOrganizations: 3, InstallationGrant: &cp.PluginEntitlementGrant{OrganizationLimit: ref.Ref(int64(3))}}}

	var diagnostics diag.Diagnostics
	addPluginManagerError(&diagnostics, "Enable", http.StatusConflict,
		[]byte(`{"error":"plugin_organization_limit","message":"The installation-wide grant allows 3 enabled organizations."}`), &installation)
	require.Len(t, diagnostics.Errors(), 1)
	require.Equal(t, "Plugin organization limit reached", diagnostics.Errors()[0].Summary())
	require.Equal(t, "Enable failed with HTTP 409 (plugin_organization_limit): The installation-wide grant allows 3 enabled organizations."+
		" Installation "+pluginFixtureInstallation+" has 3 enabled organizations and its installation-wide licence grant allows 3; disabled organizations do not count."+
		" A platform operator imports a grant with a higher limit (octl plugin-manager import-entitlements) or disables an organization, then apply again.", diagnostics.Errors()[0].Detail())

	installation.Entitlement.InstallationGrant = nil
	require.Contains(t, organizationLimitAdvice(&installation), "has 3 enabled organizations; disabled organizations do not count.")
}

// Destroying disables only an enabled organization; an organization without
// an enablement, one already disabled and a missing installation need no
// write.
func TestPluginEnablementDisableWritesOnlyForAnEnabledOrganization(t *testing.T) {
	fixture, resource := newPluginEnablementTestResource(t)
	disable := disableEnablement("Decommissioned")

	var diagnostics diag.Diagnostics
	require.NotNil(t, resource.putEnablement(t.Context(), pluginFixtureInstallation, true, &diagnostics, disable))
	fixture.change(func(installation *cp.PluginInstallation) {
		installation.Organizations = []cp.PluginEnablement{{OrgId: pluginFixtureOrg, State: cp.PluginEnablementStateDisabled, Timezone: "UTC", ApprovalPolicies: map[string]string{}}}
	})
	require.NotNil(t, resource.putEnablement(t.Context(), pluginFixtureInstallation, true, &diagnostics, disable))
	require.Nil(t, resource.putEnablement(t.Context(), uuid.NewString(), true, &diagnostics, disable))
	require.False(t, diagnostics.HasError(), "%v", diagnostics)
	fixture.inspect(func(f *pluginManagerFixture) { require.Zero(t, f.puts) })

	fixture.change(func(installation *cp.PluginInstallation) {
		installation.Organizations[0].State = cp.PluginEnablementStateEnabled
	})
	installation := resource.putEnablement(t.Context(), pluginFixtureInstallation, true, &diagnostics, disable)
	require.False(t, diagnostics.HasError(), "%v", diagnostics)
	require.Equal(t, cp.PluginEnablementStateDisabled, enablementOf(*installation, pluginFixtureOrg).State)
	fixture.inspect(func(f *pluginManagerFixture) {
		require.Equal(t, []pluginAuditEvent{{Revision: 10, OrgID: pluginFixtureOrg, State: "disabled", Timezone: "UTC", ApprovalPolicies: map[string]string{}, Reason: "Decommissioned"}}, f.audit)
	})

	resource.putEnablement(t.Context(), uuid.NewString(), false, &diagnostics, disable)
	require.Len(t, diagnostics.Errors(), 1)
	require.Equal(t, "Plugin installation not found", diagnostics.Errors()[0].Summary())
}

// Other Plugin Manager changes that keep landing between read and write end
// in a diagnostic after a bounded number of attempts.
func TestPluginEnablementGivesUpAfterRepeatedRevisionConflicts(t *testing.T) {
	fixture, resource := newPluginEnablementTestResource(t)
	fixture.inspect(func(f *pluginManagerFixture) { f.foreignWrites = 2 * pluginManagerWriteAttempts })
	enable := func(*cp.PluginEnablement) *cp.PluginEnablementPut {
		return &cp.PluginEnablementPut{Reason: "Adopt", State: cp.PluginEnablementPutStateEnabled}
	}

	var diagnostics diag.Diagnostics
	require.Nil(t, resource.putEnablement(t.Context(), pluginFixtureInstallation, false, &diagnostics, enable))
	require.Len(t, diagnostics.Errors(), 1)
	require.Equal(t, "Plugin Manager revision conflict", diagnostics.Errors()[0].Summary())
	fixture.inspect(func(f *pluginManagerFixture) {
		require.Equal(t, pluginManagerWriteAttempts, f.puts)
		require.Empty(t, f.audit)
	})
}

// The key is the command: the identical request replays, any other
// expected revision, state, reason or target is a new command.
func TestPluginManagerIdempotencyKeyIsDerivedFromTheCommand(t *testing.T) {
	t.Parallel()
	path := "/plugin-installations/" + pluginFixtureInstallation + "/organizations/" + pluginFixtureOrg
	body := cp.PluginEnablementPut{ExpectedRevision: 7, Reason: "Adopt", State: cp.PluginEnablementPutStateEnabled}
	key := pluginManagerIdempotencyKey(path, body)
	require.Regexp(t, `^terraform-[0-9a-f]{40}$`, key)
	require.Equal(t, key, pluginManagerIdempotencyKey(path, body))
	for _, other := range []string{
		pluginManagerIdempotencyKey(path, cp.PluginEnablementPut{ExpectedRevision: 8, Reason: "Adopt", State: cp.PluginEnablementPutStateEnabled}),
		pluginManagerIdempotencyKey(path, cp.PluginEnablementPut{ExpectedRevision: 7, Reason: "Adopt", State: cp.PluginEnablementPutStateDisabled}),
		pluginManagerIdempotencyKey(path, cp.PluginEnablementPut{ExpectedRevision: 7, Reason: "Adopt now", State: cp.PluginEnablementPutStateEnabled}),
		pluginManagerIdempotencyKey(path+"x", body),
	} {
		require.NotEqual(t, key, other)
	}
}
