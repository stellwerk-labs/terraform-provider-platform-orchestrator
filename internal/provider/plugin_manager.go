package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

// The Plugin Manager is the platform-scoped operator API of the Control Plane.
// Every write is a command against one global revision:
//
//	GET installation ─────────► revision R, enablement as it is
//	  │ target already holds? ─► done, nothing written
//	  ▼
//	PUT expected_revision R, Idempotency-Key derived from the request
//	  │ 409 plugin_manager_revision_conflict: another change landed ─► read again
//	  ▼
//	installation as of revision R+1
//
// pluginManagerWriteAttempts bounds how often a command is decided again
// because other Plugin Manager changes landed in between.
const pluginManagerWriteAttempts = 10

const pluginManagerRevisionConflict = "plugin_manager_revision_conflict"

const pluginInstallationNotFoundAdvice = " Install the plugin with the Plugin Manager (octl plugin-manager install) and use the installation UUID it reports, for example through the platform-orchestrator_plugin_installation data source."

// pluginManagerIdempotencyKey derives the key from the request itself. The
// identical command (same target, state, settings, reason and expected
// revision) gets the same key, so sending it again after a lost answer replays
// the recorded answer instead of changing anything twice.
func pluginManagerIdempotencyKey(path string, body cp.PluginEnablementPut) string {
	// A request body of strings, an integer and a string map always marshals.
	raw, _ := json.Marshal(body)
	sum := sha256.Sum256(append([]byte("PUT "+path+"\n"), raw...))
	return "terraform-" + hex.EncodeToString(sum[:20])
}

// enablementOf returns the installation's enablement for the organization.
func enablementOf(installation cp.PluginInstallation, orgID string) *cp.PluginEnablement {
	for i := range installation.Organizations {
		if installation.Organizations[i].OrgId == orgID {
			return &installation.Organizations[i]
		}
	}
	return nil
}

// addPluginManagerError reports a refused Plugin Manager request with its
// stable code and the operator's next step. installation is what the provider
// read before the request, if anything.
func addPluginManagerError(diagnostics *diag.Diagnostics, action string, status int, body []byte, installation *cp.PluginInstallation) {
	var problem cp.Error
	if json.Unmarshal(body, &problem) != nil || problem.Error == "" {
		diagnostics.AddError(PO_API_ERR, fmt.Sprintf("%s failed with HTTP %d: %s", action, status, body))
		return
	}
	summary, detail := PO_API_ERR, fmt.Sprintf("%s failed with HTTP %d (%s): %s", action, status, problem.Error, problem.Message)
	switch problem.Error {
	case "plugin_installation_state":
		summary, detail = "Plugin installation not active", detail+installationStateAdvice(installation)
	case "plugin_organization_limit":
		summary, detail = "Plugin organization limit reached", detail+organizationLimitAdvice(installation)
	case "plugin_work_unsettled":
		summary, detail = "Plugin work unsettled", detail+" Core still holds work of this plugin: operation grants, dispatches or Module reservations. Let the plugin finish, cancel or roll that work back, then apply again."
	case pluginManagerRevisionConflict:
		summary, detail = "Plugin Manager revision conflict", detail+fmt.Sprintf(" Other Plugin Manager changes landed during %d attempts. Apply again.", pluginManagerWriteAttempts)
	case "plugin_installation_not_found":
		summary, detail = "Plugin installation not found", detail+pluginInstallationNotFoundAdvice
	case "plugin_host_disabled":
		summary, detail = "Plugin host disabled", detail+" Enable the plugin host of this Control Plane first."
	case "plugin_manager_forbidden":
		summary, detail = "Plugin Manager forbidden", detail+" The Plugin Manager is for platform operators: reads need the platform permission plugin_manager_read, changes plugin_manager_write. Configure the provider with a token that holds them."
	}
	diagnostics.AddError(summary, detail)
}

// installationStateAdvice says how an installation becomes one that an
// organization can be enabled for: active, or disabled installation-wide.
func installationStateAdvice(installation *cp.PluginInstallation) string {
	if installation == nil {
		return ""
	}
	id := installation.InstallationId
	advice := fmt.Sprintf(" Installation %s of plugin %s is %s.", id, installation.PluginId, installation.State)
	switch installation.State {
	case cp.PluginInstallationStateInstalled:
		advice += fmt.Sprintf(" Once the plugin runtime serves it, activate it with the Plugin Manager (octl plugin-manager activate %s), then apply again.", id)
	case cp.PluginInstallationStateUninstalled:
		advice += fmt.Sprintf(" Reinstall and activate it with the Plugin Manager (octl plugin-manager reinstall %s, then activate), then apply again.", id)
	case cp.PluginInstallationStatePurged:
		advice += " A purged installation never serves again: install the plugin anew and use the new installation UUID."
	}
	if installation.Diagnostic != nil {
		advice += fmt.Sprintf(" Its last activation was refused (%s): %s", installation.Diagnostic.Code, installation.Diagnostic.Message)
	}
	return advice
}

// organizationLimitAdvice says what the installation-wide licence grant's
// organization limit needs: room, which a higher limit or a disabled
// organization makes.
func organizationLimitAdvice(installation *cp.PluginInstallation) string {
	advice := " The installation-wide licence grant limits how many organizations the installation may enable; disabled organizations do not count."
	if installation != nil && installation.Entitlement != nil {
		advice = fmt.Sprintf(" Installation %s has %d enabled organizations", installation.InstallationId, installation.Entitlement.EnabledOrganizations)
		if grant := installation.Entitlement.InstallationGrant; grant != nil && grant.OrganizationLimit != nil {
			advice += fmt.Sprintf(" and its installation-wide licence grant allows %d", *grant.OrganizationLimit)
		}
		advice += "; disabled organizations do not count."
	}
	return advice + " A platform operator imports a grant with a higher limit (octl plugin-manager import-entitlements) or disables an organization, then apply again."
}
