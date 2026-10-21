package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// rolloutsCoreFixture is an in-process Core plus Progressive Rollouts plugin.
// Its discovery and gateway decisions follow the Core plugin host rules:
//
//   - getPluginCapability reports the organization's availability state;
//   - listPluginContributions advertises an operation only when Core would
//     allow it, treating an operation with safety command values as safety;
//   - invokePluginOperation checks the concrete command again and refuses
//     with HTTP 409 and the stable plugin_* code before the plugin runs.
//
// The plugin half keeps just enough Rollout state to drive Terraform through
// create, prepare and the control commands the provider issues.
type rolloutsCoreFixture struct {
	t  *testing.T
	mu sync.Mutex

	capabilityStatus    int
	contributionsStatus int
	state               string
	adminAction         string
	features            map[string]rolloutsFeatureAccess
	listed              bool
	operations          []rolloutsFixtureOperation
	// graceUntil, when set, puts the licence feature in its grace period:
	// forward work is allowed and Core adds the grace warning.
	graceUntil string

	rollouts    map[string]*rolloutsFixtureRollout
	createKeys  map[string]string
	invocations []string
}

type rolloutsFeatureAccess struct {
	Forward bool `json:"forward"`
	Read    bool `json:"read"`
	Safety  bool `json:"safety"`
}

type rolloutsFixtureOperation struct {
	id, method     string
	mutating       bool
	safetyCommands []string
}

type rolloutsFixtureRollout struct {
	ID              string
	Spec            json.RawMessage
	State           string
	ResourceVersion int64
	PlanRevision    int
}

const (
	rolloutsFixtureOrg     = "local-test-org"
	rolloutsFixtureToken   = "local-test-token"
	rolloutsFixtureFeature = "progressive-rollouts"
)

// rolloutsFixtureManifest installs the plugin exactly as its published
// contract declares it: every operation with its method, mutating flag,
// licence feature and safety command values.
func rolloutsFixtureManifest(t *testing.T) []rolloutsFixtureOperation {
	t.Helper()
	var manifest []rolloutsFixtureOperation
	for id, operation := range progressiveRolloutsContractOperations(t, loadContractDocument(t, progressiveRolloutsSpecPath)) {
		method, methodOK := operation["x-method"].(string)
		mutating, mutatingOK := operation["x-mutating"].(bool)
		if !methodOK || !mutatingOK || operation["x-required-feature"] != rolloutsFixtureFeature {
			t.Fatalf("operation %s has incomplete contract metadata", id)
		}
		installed := rolloutsFixtureOperation{id: id, method: method, mutating: mutating}
		if safety, ok := operation["x-safety-path-values"].(map[string]any); ok {
			values, _ := safety["rolloutCommand"].([]any)
			for _, value := range values {
				command, _ := value.(string)
				installed.safetyCommands = append(installed.safetyCommands, command)
			}
		}
		manifest = append(manifest, installed)
	}
	return manifest
}

func (f *rolloutsCoreFixture) operation(id string) *rolloutsFixtureOperation {
	for index := range f.operations {
		if f.operations[index].id == id {
			return &f.operations[index]
		}
	}
	f.t.Fatalf("operation %s is not installed", id)
	return nil
}

// withdraw models an installed plugin version without the operation.
func (f *rolloutsCoreFixture) withdraw(id string) {
	f.operations = slices.DeleteFunc(f.operations, func(operation rolloutsFixtureOperation) bool { return operation.id == id })
}

func newRolloutsCoreFixture(t *testing.T) (*rolloutsCoreFixture, *httptest.Server) {
	t.Helper()
	fixture := &rolloutsCoreFixture{
		t: t, capabilityStatus: http.StatusOK, contributionsStatus: http.StatusOK,
		state: "available", listed: true, operations: rolloutsFixtureManifest(t),
		features:   map[string]rolloutsFeatureAccess{rolloutsFixtureFeature: {Forward: true, Read: true, Safety: true}},
		rollouts:   map[string]*rolloutsFixtureRollout{},
		createKeys: map[string]string{},
	}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	return fixture, server
}

// configure changes what Core reports, safely while requests are in flight.
func (f *rolloutsCoreFixture) configure(change func(*rolloutsCoreFixture)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// expireLicence models a signed licence that ended: Core keeps reads and
// safety work, withdraws forward work and reports plugin_not_entitled.
func (f *rolloutsCoreFixture) expireLicence() {
	f.configure(func(f *rolloutsCoreFixture) {
		f.state, f.graceUntil = "not_entitled", ""
		f.adminAction = "Ask an administrator to install or renew a signed licence for this plugin and organization."
		f.features = map[string]rolloutsFeatureAccess{rolloutsFixtureFeature: {Read: true, Safety: true}}
	})
}

// enterGrace models a signed licence in its grace period: everything stays
// allowed and discovery and invocations carry Core's grace warning.
func (f *rolloutsCoreFixture) enterGrace(graceUntil string) {
	f.configure(func(f *rolloutsCoreFixture) {
		f.state, f.adminAction = "available", ""
		f.features = map[string]rolloutsFeatureAccess{rolloutsFixtureFeature: {Forward: true, Read: true, Safety: true}}
		f.graceUntil = graceUntil
	})
}

func (f *rolloutsCoreFixture) graceWarning() map[string]any {
	return map[string]any{
		"code": "plugin_entitlement_grace", "grace_until": f.graceUntil, "features": []string{rolloutsFixtureFeature},
		"message": "The signed plugin licence is in its grace period until " + f.graceUntil + ". Forward work continues until then; ask an administrator to renew the licence.",
	}
}

// allows mirrors Core's decision for one licence feature.
func (f *rolloutsCoreFixture) allows(mutating, safety bool) bool {
	if f.state != "available" && f.state != "not_enabled" && f.state != "not_entitled" {
		return false
	}
	if f.features == nil {
		return f.state == "available" || !mutating || safety
	}
	access, found := f.features[rolloutsFixtureFeature]
	if !found {
		return false
	}
	if !mutating {
		return access.Read
	}
	if safety {
		return access.Safety
	}
	return f.state == "available" && access.Forward
}

func (f *rolloutsCoreFixture) mode() string {
	switch f.state {
	case "available":
		return "full"
	case "not_enabled", "not_entitled":
		return "safety_only"
	default:
		return "hidden"
	}
}

func (f *rolloutsCoreFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+rolloutsFixtureToken {
		writeJSON(f.t, w, http.StatusUnauthorized, map[string]string{"error": "API-401", "message": "unauthorized"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	base := "/orgs/" + rolloutsFixtureOrg + "/plugins"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == base+"/"+progressiveRolloutsPluginID:
		f.serveCapability(w)
	case r.Method == http.MethodGet && r.URL.Path == base:
		f.serveContributions(w)
	case r.Method == http.MethodPost && r.URL.Path == base+"/"+progressiveRolloutsPluginID+"/invoke":
		f.serveInvocation(w, r)
	default:
		writeJSON(f.t, w, http.StatusNotFound, map[string]string{"error": "API-404", "message": "not found"})
	}
}

func (f *rolloutsCoreFixture) entitlement() map[string]any {
	if f.features == nil {
		return nil
	}
	features := map[string]any{}
	for name, access := range f.features {
		features[name] = map[string]any{"state": "active", "trust_class": "development", "forward": access.Forward, "read": access.Read, "safety": access.Safety}
		if f.graceUntil != "" {
			features[name] = map[string]any{"state": "grace", "grace_until": f.graceUntil, "trust_class": "development", "forward": access.Forward, "read": access.Read, "safety": access.Safety}
		}
	}
	entitlement := map[string]any{"features": features}
	if f.graceUntil != "" {
		entitlement["warning"] = f.graceWarning()
	}
	return entitlement
}

func (f *rolloutsCoreFixture) serveCapability(w http.ResponseWriter) {
	if f.capabilityStatus != http.StatusOK {
		writeJSON(f.t, w, f.capabilityStatus, map[string]string{"error": "API-000", "message": http.StatusText(f.capabilityStatus)})
		return
	}
	capability := map[string]any{"id": progressiveRolloutsPluginID, "display_name": progressiveRolloutsPluginDisplayName, "state": f.state, "capabilities": []string{}}
	if f.adminAction != "" {
		capability["admin_action"] = f.adminAction
	}
	if entitlement := f.entitlement(); entitlement != nil {
		capability["entitlement"] = entitlement
	}
	writeJSON(f.t, w, http.StatusOK, capability)
}

func (f *rolloutsCoreFixture) serveContributions(w http.ResponseWriter) {
	if f.contributionsStatus != http.StatusOK {
		writeJSON(f.t, w, f.contributionsStatus, map[string]string{"error": "API-000", "message": http.StatusText(f.contributionsStatus)})
		return
	}
	items := []map[string]any{}
	if f.listed {
		operations := []map[string]any{}
		for _, operation := range f.operations {
			if f.allows(operation.mutating, len(operation.safetyCommands) > 0) {
				operations = append(operations, map[string]any{"id": operation.id, "method": operation.method, "mutating": operation.mutating, "required_feature": rolloutsFixtureFeature})
			}
		}
		item := map[string]any{"id": progressiveRolloutsPluginID, "display_name": progressiveRolloutsPluginDisplayName, "version": "1.0.0", "state": f.state, "mode": f.mode(), "operations": operations}
		if entitlement := f.entitlement(); entitlement != nil {
			item["entitlement"] = entitlement
		}
		items = append(items, item)
	}
	writeJSON(f.t, w, http.StatusOK, map[string]any{"items": items})
}

func (f *rolloutsCoreFixture) refuse(w http.ResponseWriter, status int, code, message string) {
	writeJSON(f.t, w, status, map[string]string{"error": code, "message": message})
}

func (f *rolloutsCoreFixture) serveInvocation(w http.ResponseWriter, r *http.Request) {
	var invocation struct {
		OperationID    string            `json:"operation_id"`
		PathParameters map[string]string `json:"path_parameters"`
		Body           json.RawMessage   `json:"body"`
		IdempotencyKey string            `json:"idempotency_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&invocation); err != nil {
		f.refuse(w, http.StatusBadRequest, "plugin_request_invalid", err.Error())
		return
	}
	record := invocation.OperationID
	if command := invocation.PathParameters["rolloutCommand"]; command != "" {
		record += ":" + command
	}
	f.invocations = append(f.invocations, record)
	if f.state == "not_installed" || !f.listed {
		f.refuse(w, http.StatusNotFound, "plugin_not_installed", "The plugin is not installed for this organization.")
		return
	}
	index := slices.IndexFunc(f.operations, func(operation rolloutsFixtureOperation) bool { return operation.id == invocation.OperationID })
	if index < 0 {
		f.refuse(w, http.StatusBadRequest, "plugin_operation_unknown", "This operation is not registered by the installed plugin.")
		return
	}
	operation := f.operations[index]
	if !f.allows(operation.mutating, slices.Contains(operation.safetyCommands, invocation.PathParameters["rolloutCommand"])) {
		code := "plugin_" + f.state
		if f.state == "available" {
			code = "plugin_not_entitled"
		}
		f.refuse(w, http.StatusConflict, code, "The plugin cannot accept this operation. Ask an administrator to check its availability and signed licence.")
		return
	}
	if operation.mutating && invocation.IdempotencyKey == "" {
		f.refuse(w, http.StatusBadRequest, "plugin_idempotency_required", "A non-empty idempotency key (up to 128 characters) is required.")
		return
	}
	status, body := f.plugin(invocation.OperationID, invocation.PathParameters, invocation.Body, invocation.IdempotencyKey)
	result := map[string]any{"status": status, "body": body}
	if f.graceUntil != "" {
		result["entitlement_warning"] = f.graceWarning()
	}
	writeJSON(f.t, w, http.StatusOK, result)
}

// pluginError is the plugin's own error body, ProgressiveRolloutError.
func pluginError(code, message string) map[string]string {
	return map[string]string{"code": code, "message": message}
}

func (f *rolloutsCoreFixture) plugin(operationID string, path map[string]string, body json.RawMessage, key string) (int, any) {
	if operationID == "createProgressiveRollout" {
		if id, seen := f.createKeys[key]; seen {
			return http.StatusCreated, f.rolloutJSON(f.rollouts[id])
		}
		rollout := &rolloutsFixtureRollout{ID: uuid.NewString(), Spec: body, State: "Draft", ResourceVersion: 1}
		f.rollouts[rollout.ID], f.createKeys[key] = rollout, rollout.ID
		return http.StatusCreated, f.rolloutJSON(rollout)
	}
	if operationID == "previewProgressiveRollout" {
		return http.StatusOK, f.planJSON(1)
	}
	rollout, found := f.rollouts[path["rolloutId"]]
	if !found {
		return http.StatusNotFound, pluginError("not_found", "Rollout not found.")
	}
	if operationID == "getProgressiveRollout" {
		return http.StatusOK, f.detailJSON(rollout)
	}
	var request struct {
		ExpectedResourceVersion int64           `json:"expected_resource_version"`
		Spec                    json.RawMessage `json:"spec"`
		CandidateFingerprint    string          `json:"candidate_fingerprint"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return http.StatusBadRequest, pluginError("invalid_request", err.Error())
	}
	if request.ExpectedResourceVersion != rollout.ResourceVersion {
		return http.StatusConflict, pluginError("conflict", fmt.Sprintf("expected resource version %d, current %d", request.ExpectedResourceVersion, rollout.ResourceVersion))
	}
	transitions := map[string]map[string]string{
		"start":               {"Ready": "Running"},
		"return-to-draft":     {"Ready": "Draft"},
		"pause":               {"Running": "Paused"},
		"resume":              {"Paused": "Running"},
		"cancel":              {"Draft": "Cancelled", "Ready": "Cancelled", "Running": "Cancelled", "Paused": "Cancelled"},
		"cancel-and-rollback": {"Running": "RolledBack", "Paused": "RolledBack"},
		"rollback":            {"Succeeded": "RolledBack", "Failed": "RolledBack", "Cancelled": "RolledBack"},
	}
	switch operationID {
	case "updateProgressiveRolloutDraft":
		if rollout.State != "Draft" {
			return http.StatusConflict, pluginError("conflict", "Only a Draft can be edited.")
		}
		rollout.Spec = request.Spec
	case "prepareProgressiveRollout":
		if rollout.State != "Draft" || request.CandidateFingerprint != f.planJSON(1)["fingerprint"] {
			return http.StatusConflict, pluginError("conflict", "The previewed Plan is stale.")
		}
		rollout.State, rollout.PlanRevision = "Ready", 1
	case "commandProgressiveRollout":
		next, allowed := transitions[path["rolloutCommand"]][rollout.State]
		if !allowed {
			return http.StatusConflict, pluginError("conflict", fmt.Sprintf("%s is not allowed in %s", path["rolloutCommand"], rollout.State))
		}
		rollout.State = next
	}
	rollout.ResourceVersion++
	if operationID == "updateProgressiveRolloutDraft" {
		return http.StatusOK, f.rolloutJSON(rollout)
	}
	return http.StatusOK, f.detailJSON(rollout)
}

func (f *rolloutsCoreFixture) planJSON(revision int) map[string]any {
	return map[string]any{
		"revision": revision, "fingerprint": "sha256:plan-1", "created_at": time.Unix(0, 0).UTC().Format(time.RFC3339),
		"default_promotion": "none", "changes": []any{}, "waves": []any{}, "environments": []any{}, "promotions": []any{},
	}
}

func (f *rolloutsCoreFixture) rolloutJSON(rollout *rolloutsFixtureRollout) map[string]any {
	value := map[string]any{
		"id": rollout.ID, "org_id": rolloutsFixtureOrg, "spec": rollout.Spec, "state": rollout.State,
		"status": map[string]any{"message": "local fixture"}, "resource_version": rollout.ResourceVersion,
		"execution_started": rollout.State != "Draft" && rollout.State != "Ready",
		"created_by":        "00000000-0000-4000-8000-000000000001", "created_at": time.Unix(0, 0).UTC().Format(time.RFC3339), "updated_at": time.Unix(0, 0).UTC().Format(time.RFC3339),
	}
	if rollout.PlanRevision > 0 {
		value["current_plan_revision"] = rollout.PlanRevision
	}
	return value
}

func (f *rolloutsCoreFixture) detailJSON(rollout *rolloutsFixtureRollout) map[string]any {
	detail := map[string]any{"rollout": f.rolloutJSON(rollout), "executions": []any{}, "approvals": []any{}, "conflicts": []any{}, "events": []any{}, "metrics": map[string]any{}}
	if rollout.PlanRevision > 0 {
		detail["plan"] = f.planJSON(rollout.PlanRevision)
	}
	return detail
}

// operationsInvoked lists every invocation that reached the Core gateway as
// operationId or operationId:command, including refused ones.
func (f *rolloutsCoreFixture) operationsInvoked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.invocations)
}
