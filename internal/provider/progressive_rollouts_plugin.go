package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
	rollouts "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/progressive-rollouts"
)

// Progressive Rollouts is a Stellwerk Enterprise plugin. The provider ships its
// resources, but only Core decides whether the plugin may serve a request:
//
//	provider --getPluginCapability-----> Core   state and administrator action
//	provider --listPluginContributions-> Core   operations this org may call now
//	provider --invokePluginOperation---> Core --> plugin   (by operation ID only)
//
// Core advertises only the operations that installation, enablement and licence
// currently permit. After a licence expires, reads and safety commands remain
// advertised while forward-only mutations disappear. The provider never
// classifies plugin operations itself; it turns Core's answer into a stable
// plugin_* diagnostic before any plugin work starts, and Core still enforces
// every invocation.
const (
	progressiveRolloutsPluginID          = "dev.stellwerk.progressive-rollouts"
	progressiveRolloutsPluginDisplayName = "Progressive Rollouts"
	progressiveRolloutsPluginTitle       = "Stellwerk Enterprise plugin " + progressiveRolloutsPluginDisplayName + " (" + progressiveRolloutsPluginID + ")"
	progressiveRolloutsUnavailableTitle  = "Required Stellwerk plugin unavailable"
)

type progressiveRolloutsOperation struct {
	method   string
	mutating bool
}

// progressiveRolloutsOperations is the exact set of plugin operations this
// provider invokes. Anything else is refused locally, so the provider never
// becomes a generic plugin proxy.
var progressiveRolloutsOperations = map[string]progressiveRolloutsOperation{
	"getProgressiveRollout":         {method: http.MethodGet},
	"previewProgressiveRollout":     {method: http.MethodPost},
	"createProgressiveRollout":      {method: http.MethodPost, mutating: true},
	"updateProgressiveRolloutDraft": {method: http.MethodPatch, mutating: true},
	"prepareProgressiveRollout":     {method: http.MethodPost, mutating: true},
	"commandProgressiveRollout":     {method: http.MethodPost, mutating: true},
}

var progressiveRolloutsStateCodes = map[cp.PluginAvailabilityState]string{
	cp.PluginAvailabilityStateNotInstalled: "plugin_not_installed",
	cp.PluginAvailabilityStateNotEnabled:   "plugin_not_enabled",
	cp.PluginAvailabilityStateNotEntitled:  "plugin_not_entitled",
	cp.PluginAvailabilityStateIncompatible: "plugin_incompatible",
	cp.PluginAvailabilityStateUnavailable:  "plugin_unavailable",
}

var progressiveRolloutsRemedies = map[string]string{
	"plugin_not_installed": "Ask a platform administrator to install the plugin and enable it for this organization.",
	"plugin_not_enabled":   "Ask an administrator to enable the plugin for this organization.",
	"plugin_not_entitled":  "Ask an administrator to install or renew a signed licence for this organization. Reads and safety commands remain available.",
	"plugin_incompatible":  "Ask an administrator to install a plugin version that is compatible with this Core and provider.",
	"plugin_unavailable":   "Retry later, or ask an administrator to check the plugin workload.",
}

type progressiveRolloutsClient struct {
	cp    cp.ClientWithResponsesInterface
	orgID string
}

func addProgressiveRolloutsUnavailable(diagnostics *diag.Diagnostics, code, reason, adminAction string) {
	remedy := adminAction
	if remedy == "" {
		remedy = progressiveRolloutsRemedies[code]
	}
	diagnostics.AddError(progressiveRolloutsUnavailableTitle, fmt.Sprintf("%s: %s %s. %s", code, progressiveRolloutsPluginTitle, reason, remedy))
}

// pluginAvailabilityCode maps a Core availability state to its stable code.
// Unknown future states are never treated as available.
func pluginAvailabilityCode(state cp.PluginAvailabilityState) string {
	if code, known := progressiveRolloutsStateCodes[state]; known {
		return code
	}
	return "plugin_unavailable"
}

func addProgressiveRolloutsStateError(diagnostics *diag.Diagnostics, capability cp.PluginCapability) {
	adminAction := ""
	if capability.AdminAction != nil {
		adminAction = *capability.AdminAction
	}
	addProgressiveRolloutsUnavailable(diagnostics, pluginAvailabilityCode(capability.State), fmt.Sprintf("is %s for this organization", capability.State), adminAction)
}

// addProgressiveRolloutsDiscoveryError reports a failed discovery request. A
// server that predates plugin discovery answers 404 or 501; that means the
// plugin cannot be installed there, not an unexplained missing route.
func addProgressiveRolloutsDiscoveryError(diagnostics *diag.Diagnostics, status int, body []byte) {
	switch status {
	case http.StatusNotFound, http.StatusNotImplemented:
		addProgressiveRolloutsUnavailable(diagnostics, "plugin_not_installed", "is not supported by this server, which predates plugin capability discovery", "")
	case http.StatusServiceUnavailable:
		addProgressiveRolloutsUnavailable(diagnostics, "plugin_unavailable", "cannot be discovered right now", "")
	default:
		addAPIResponseError(diagnostics, "discover the "+progressiveRolloutsPluginDisplayName+" plugin", status, body)
	}
}

// require reports whether Core currently permits every listed operation for
// this organization and adds a stable diagnostic when it does not.
func (c progressiveRolloutsClient) require(ctx context.Context, diagnostics *diag.Diagnostics, operationIDs ...string) bool {
	capabilityResponse, err := c.cp.GetPluginCapabilityWithResponse(ctx, c.orgID, progressiveRolloutsPluginID)
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, "Unable to discover the "+progressiveRolloutsPluginDisplayName+" plugin: "+err.Error())
		return false
	}
	if capabilityResponse.StatusCode() != http.StatusOK || capabilityResponse.JSON200 == nil {
		addProgressiveRolloutsDiscoveryError(diagnostics, capabilityResponse.StatusCode(), capabilityResponse.Body)
		return false
	}
	capability := *capabilityResponse.JSON200
	if capability.Entitlement != nil {
		addPluginEntitlementWarning(diagnostics, capability.Entitlement.Warning)
	}
	switch capability.State {
	case cp.PluginAvailabilityStateAvailable, cp.PluginAvailabilityStateNotEnabled, cp.PluginAvailabilityStateNotEntitled:
		// Core may still serve reads and safety commands; its advertised
		// operations below decide which ones.
	default:
		addProgressiveRolloutsStateError(diagnostics, capability)
		return false
	}

	contributionsResponse, err := c.cp.ListPluginContributionsWithResponse(ctx, c.orgID)
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, "Unable to discover "+progressiveRolloutsPluginDisplayName+" operations: "+err.Error())
		return false
	}
	if contributionsResponse.StatusCode() != http.StatusOK || contributionsResponse.JSON200 == nil {
		addProgressiveRolloutsDiscoveryError(diagnostics, contributionsResponse.StatusCode(), contributionsResponse.Body)
		return false
	}
	var contribution *cp.PluginContribution
	for index := range contributionsResponse.JSON200.Items {
		if contributionsResponse.JSON200.Items[index].Id == progressiveRolloutsPluginID {
			contribution = &contributionsResponse.JSON200.Items[index]
			break
		}
	}
	if contribution == nil {
		addProgressiveRolloutsUnavailable(diagnostics, "plugin_unavailable", "is installed but Core does not route any of its operations", "")
		return false
	}
	if contribution.Entitlement != nil {
		addPluginEntitlementWarning(diagnostics, contribution.Entitlement.Warning)
	}

	for _, operationID := range operationIDs {
		expected, known := progressiveRolloutsOperations[operationID]
		if !known {
			diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("The provider does not permit %s operation %q.", progressiveRolloutsPluginDisplayName, operationID))
			return false
		}
		advertised := false
		for _, operation := range contribution.Operations {
			if operation.Id != operationID {
				continue
			}
			if operation.Method != expected.method || operation.Mutating != expected.mutating {
				addProgressiveRolloutsUnavailable(diagnostics, "plugin_incompatible", fmt.Sprintf("advertises operation %q as %s (mutating %t), but this provider requires %s (mutating %t)", operationID, operation.Method, operation.Mutating, expected.method, expected.mutating), "")
				return false
			}
			advertised = true
			break
		}
		if advertised {
			continue
		}
		switch {
		case capability.State != cp.PluginAvailabilityStateAvailable:
			// Licence expiry or disablement: Core withdrew a forward operation.
			addProgressiveRolloutsStateError(diagnostics, capability)
		case progressiveRolloutsLicenceRestricted(capability.Entitlement):
			addProgressiveRolloutsUnavailable(diagnostics, "plugin_not_entitled", fmt.Sprintf("does not license operation %q for this organization", operationID), "")
		default:
			addProgressiveRolloutsUnavailable(diagnostics, "plugin_incompatible", fmt.Sprintf("does not provide operation %q in the installed version", operationID), "")
		}
		return false
	}
	return true
}

const pluginGraceWarningSummary = "Stellwerk plugin licence in grace period"

// addPluginEntitlementWarning shows Core's licence grace warning once per
// operation, however many discovery and invoke responses repeat it. It never
// changes the outcome: while a feature is in grace, forward work succeeds.
func addPluginEntitlementWarning(diagnostics *diag.Diagnostics, warning *cp.PluginEntitlementWarning) {
	if warning == nil {
		return
	}
	for _, existing := range diagnostics.Warnings() {
		if existing.Summary() == pluginGraceWarningSummary {
			return
		}
	}
	diagnostics.AddWarning(pluginGraceWarningSummary, fmt.Sprintf("%s (%s, grace until %s, features: %s)",
		warning.Message, warning.Code, warning.GraceUntil.UTC().Format(time.RFC3339), strings.Join(warning.Features, ", ")))
}

// progressiveRolloutsLicenceRestricted reports whether any declared licence
// feature currently denies forward work, which explains a missing operation
// on an otherwise available plugin.
func progressiveRolloutsLicenceRestricted(entitlement *cp.PluginEntitlementStatus) bool {
	if entitlement == nil {
		return false
	}
	for _, access := range entitlement.Features {
		if !access.Forward {
			return true
		}
	}
	return false
}

// addProgressiveRolloutsPluginError reports a plugin refusal. The plugin
// contract wraps every failure in ProgressiveRolloutError, whose code is
// stable; plugin_* codes keep the plugin-availability title.
func addProgressiveRolloutsPluginError(diagnostics *diag.Diagnostics, action string, status int, body []byte) {
	var problem rollouts.ProgressiveRolloutError
	if err := json.Unmarshal(body, &problem); err != nil || problem.Code == "" {
		addAPIResponseError(diagnostics, action, status, body)
		return
	}
	title := PO_API_ERR
	if strings.HasPrefix(problem.Code, "plugin_") {
		title = progressiveRolloutsUnavailableTitle
	}
	message := problem.Code
	if problem.Message != nil && *problem.Message != "" {
		message = *problem.Message
	}
	diagnostics.AddError(title, fmt.Sprintf("%s failed with plugin HTTP %d (%s): %s", action, status, problem.Code, message))
}

type progressiveRolloutsCall struct {
	// action completes "Unable to ..." in diagnostics.
	action         string
	operationID    string
	pathParameters map[string]string
	body           any
	idempotencyKey string
	expectedStatus int
}

// invokeProgressiveRollouts sends one operation through the Core plugin gateway
// and decodes the plugin response into T.
func invokeProgressiveRollouts[T any](ctx context.Context, client progressiveRolloutsClient, call progressiveRolloutsCall, diagnostics *diag.Diagnostics) (T, bool) {
	var result T
	operation, known := progressiveRolloutsOperations[call.operationID]
	if !known {
		diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("The provider does not permit %s operation %q.", progressiveRolloutsPluginDisplayName, call.operationID))
		return result, false
	}
	if operation.mutating && strings.TrimSpace(call.idempotencyKey) == "" {
		diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Unable to %s: an idempotency key is required for %s.", call.action, call.operationID))
		return result, false
	}
	invocation := cp.PluginInvocation{OperationId: call.operationID}
	if call.idempotencyKey != "" {
		invocation.IdempotencyKey = &call.idempotencyKey
	}
	if len(call.pathParameters) != 0 {
		invocation.PathParameters = &call.pathParameters
	}
	if call.body != nil {
		encoded, err := json.Marshal(call.body)
		if err != nil {
			diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Unable to encode the %s request: %s", call.operationID, err))
			return result, false
		}
		raw := json.RawMessage(encoded)
		invocation.Body = &raw
	}
	response, err := client.cp.InvokePluginOperationWithResponse(ctx, client.orgID, progressiveRolloutsPluginID, invocation)
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to %s: %s", call.action, err))
		return result, false
	}
	// Core gateway refusals carry the stable plugin_* code in the error envelope.
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		addAPIResponseError(diagnostics, call.action, response.StatusCode(), response.Body)
		return result, false
	}
	addPluginEntitlementWarning(diagnostics, response.JSON200.EntitlementWarning)
	if response.JSON200.Status != call.expectedStatus {
		addProgressiveRolloutsPluginError(diagnostics, call.action, response.JSON200.Status, response.JSON200.Body)
		return result, false
	}
	if err := json.Unmarshal(response.JSON200.Body, &result); err != nil {
		diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Unable to decode the %s response: %s", call.operationID, err))
		return result, false
	}
	return result, true
}
