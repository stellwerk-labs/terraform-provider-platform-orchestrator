package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/ref"
)

const (
	pluginFixtureOrg          = "local-test-org"
	pluginFixtureToken        = "local-test-token"
	pluginFixturePlugin       = "dev.stellwerk.progressive-rollouts"
	pluginFixtureInstallation = "55555555-5555-4555-8555-555555555555"
	pluginFixturePurged       = "66666666-6666-4666-8666-666666666666"
)

// pluginAuditEvent is what the fixture records for every committed change.
type pluginAuditEvent struct {
	Revision         int64
	OrgID            string
	State            string
	Timezone         string
	ApprovalPolicies map[string]string
	Reason           string
}

// pluginManagerFixture is a Control Plane Plugin Manager with one installation
// of a plugin and its purged predecessor. Like the real Plugin Manager, it
// serialises writes on one revision: it replays the answer of a known
// Idempotency-Key, refuses another expected_revision with 409
// plugin_manager_revision_conflict, refuses enabling an installation that is
// neither active nor disabled and an enablement beyond the installation-wide
// grant's organization limit, and changes nothing (no revision, no audit event) when the target already holds.
type pluginManagerFixture struct {
	t  *testing.T
	mu sync.Mutex

	revision     int64
	installation cp.PluginInstallation
	purged       cp.PluginInstallation

	// organizationLimit, when set, is the installation-wide grant's limit of
	// enabled organizations.
	organizationLimit int

	// foreignWrites lands that many changes of other operators, one right
	// before each of the next writes.
	foreignWrites int

	commands  map[string]pluginCommand
	audit     []pluginAuditEvent
	puts      int
	conflicts int
}

type pluginCommand struct {
	fingerprint string
	answer      cp.PluginInstallation
}

func newPluginManagerFixture(t *testing.T) (*pluginManagerFixture, *httptest.Server) {
	t.Helper()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	installation := func(id, state string) cp.PluginInstallation {
		return cp.PluginInstallation{
			InstallationId: uuid.MustParse(id), PluginId: pluginFixturePlugin, PackageSha256: strings.Repeat("ab", 32),
			State: cp.PluginInstallationState(state), BackendUrl: "https://rollouts.example.com", Organizations: []cp.PluginEnablement{},
			OrganizationMode: cp.PluginOrganizationModeSelected, OrganizationDefaults: cp.PluginOrganizationDefaults{Timezone: "UTC", ApprovalPolicies: map[string]string{}},
			CreatedAt: created, UpdatedAt: created, CreatedBy: uuid.New(), UpdatedBy: uuid.New(),
		}
	}
	fixture := &pluginManagerFixture{
		t: t, revision: 7, commands: map[string]pluginCommand{},
		installation: installation(pluginFixtureInstallation, "active"),
		purged:       installation(pluginFixturePurged, "purged"),
	}
	fixture.installation.Availability = cp.PluginAvailabilityStateAvailable
	fixture.purged.Availability = cp.PluginAvailabilityStateNotInstalled
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	return fixture, server
}

// change applies an operator's change outside Terraform as one revision.
func (f *pluginManagerFixture) change(apply func(installation *cp.PluginInstallation)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	apply(&f.installation)
	f.revision++
}

func (f *pluginManagerFixture) inspect(read func(f *pluginManagerFixture)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	read(f)
}

func (f *pluginManagerFixture) enabledOrganizations() int {
	enabled := 0
	for _, enablement := range f.installation.Organizations {
		if enablement.State == cp.PluginEnablementStateEnabled {
			enabled++
		}
	}
	return enabled
}

func (f *pluginManagerFixture) answer() cp.PluginInstallation {
	answer := f.installation
	answer.Revision = ref.Ref(f.revision)
	answer.Organizations = make([]cp.PluginEnablement, len(f.installation.Organizations))
	for i, enablement := range f.installation.Organizations {
		enablement.ApprovalPolicies = maps.Clone(enablement.ApprovalPolicies)
		answer.Organizations[i] = enablement
	}
	return answer
}

func (f *pluginManagerFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		assert.NoError(f.t, json.NewEncoder(w).Encode(value))
	}
	refuse := func(status int, code, message string) {
		write(status, cp.Error{Error: code, Message: message})
	}
	if r.Header.Get("Authorization") != "Bearer "+pluginFixtureToken {
		refuse(http.StatusUnauthorized, "API-401", "unauthorized")
		return
	}
	installationPath := "/plugin-installations/" + pluginFixtureInstallation
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/plugin-installations":
		purged := f.purged
		write(http.StatusOK, cp.PluginInstallationPage{Revision: f.revision, Items: []cp.PluginInstallation{purged, f.answer()}})
	case r.Method == http.MethodGet && r.URL.Path == installationPath:
		write(http.StatusOK, f.answer())
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/plugin-installations/"):
		refuse(http.StatusNotFound, "plugin_installation_not_found", "The plugin installation does not exist.")
	case r.Method == http.MethodPut && r.URL.Path == installationPath+"/organizations/"+pluginFixtureOrg:
		f.putEnablement(r, write, refuse)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		refuse(http.StatusNotFound, "API-404", "not found")
	}
}

func (f *pluginManagerFixture) putEnablement(r *http.Request, write func(int, any), refuse func(int, string, string)) {
	f.puts++
	key := r.Header.Get("Idempotency-Key")
	assert.Regexp(f.t, `^terraform-[0-9a-f]{40}$`, key)
	raw := new(bytes.Buffer)
	_, err := raw.ReadFrom(r.Body)
	assert.NoError(f.t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw.Bytes()))
	decoder.DisallowUnknownFields()
	var body cp.PluginEnablementPut
	if !assert.NoError(f.t, decoder.Decode(&body)) {
		refuse(http.StatusBadRequest, "plugin_manager_request_invalid", "invalid body")
		return
	}
	if command, known := f.commands[key]; known {
		if command.fingerprint != raw.String() {
			refuse(http.StatusConflict, "plugin_manager_idempotency_conflict", "The idempotency key was already used for a different Plugin Manager command.")
			return
		}
		write(http.StatusOK, command.answer)
		return
	}
	if f.foreignWrites > 0 {
		f.foreignWrites--
		f.revision++
	}
	if body.ExpectedRevision != f.revision {
		f.conflicts++
		refuse(http.StatusConflict, pluginManagerRevisionConflict, fmt.Sprintf("The Plugin Manager is at revision %d, not at the expected revision %d. Read the current state and retry.", f.revision, body.ExpectedRevision))
		return
	}
	if strings.TrimSpace(body.Reason) == "" {
		refuse(http.StatusBadRequest, "plugin_manager_request_invalid", "A reason is required.")
		return
	}
	if f.installation.State != cp.PluginInstallationStateActive && f.installation.State != cp.PluginInstallationStateDisabled {
		refuse(http.StatusConflict, "plugin_installation_state", "Activate the installation before enabling an organization.")
		return
	}
	target := cp.PluginEnablement{OrgId: pluginFixtureOrg, State: cp.PluginEnablementState(body.State), Timezone: "UTC", ApprovalPolicies: map[string]string{}}
	if body.Timezone != nil && *body.Timezone != "" {
		target.Timezone = *body.Timezone
	}
	if body.ApprovalPolicies != nil {
		target.ApprovalPolicies = maps.Clone(*body.ApprovalPolicies)
	}
	current := enablementOf(f.installation, pluginFixtureOrg)
	switch {
	case current != nil && current.State == target.State && current.Timezone == target.Timezone && maps.Equal(current.ApprovalPolicies, target.ApprovalPolicies):
	case target.State == cp.PluginEnablementStateEnabled && (current == nil || current.State != cp.PluginEnablementStateEnabled) && f.enabledOrganizations() >= f.organizationLimit && f.organizationLimit > 0:
		refuse(http.StatusConflict, "plugin_organization_limit", fmt.Sprintf("The installation-wide grant allows %d enabled organizations.", f.organizationLimit))
		return
	default:
		target.CreatedAt, target.UpdatedAt = time.Now().UTC(), time.Now().UTC()
		target.CapabilityRegistration = cp.PluginCapabilityRegistration{State: cp.PluginCapabilityRegistrationStateRegistered}
		if current != nil {
			*current = target
		} else {
			f.installation.Organizations = append(f.installation.Organizations, target)
		}
		f.revision++
		f.audit = append(f.audit, pluginAuditEvent{Revision: f.revision, OrgID: target.OrgId, State: string(target.State), Timezone: target.Timezone, ApprovalPolicies: target.ApprovalPolicies, Reason: body.Reason})
	}
	answer := f.answer()
	f.commands[key] = pluginCommand{fingerprint: raw.String(), answer: answer}
	write(http.StatusOK, answer)
}

func pluginEnablementConfig(apiURL, reason, settings string) string {
	return fmt.Sprintf(`
provider "platform-orchestrator" {
  api_url    = %[1]q
  org_id     = %[2]q
  auth_token = %[3]q
}

data "platform-orchestrator_plugin_installation" "rollouts" {
  plugin_id = %[4]q
}

resource "platform-orchestrator_plugin_enablement" "rollouts" {
  installation_id = data.platform-orchestrator_plugin_installation.rollouts.installation_id
  reason          = %[5]q
%[6]s
}
`, apiURL, pluginFixtureOrg, pluginFixtureToken, pluginFixturePlugin, reason, settings)
}

// TestAccPluginEnablementLocal is the provider's plugin enablement acceptance:
// creating enables the organization, a reason-only change writes nothing,
// settings converge through concurrent Plugin Manager changes, import reads
// the server state, drift is enabled again, refusals name their state and
// destroying disables without removing the enablement.
func TestAccPluginEnablementLocal(t *testing.T) {
	fixture, server := newPluginManagerFixture(t)
	const address = "platform-orchestrator_plugin_enablement.rollouts"
	const lookup = "data.platform-orchestrator_plugin_installation.rollouts"
	settings := `  timezone          = "Europe/Berlin"
  approval_policies = { environment = "rollouts.approve" }`
	auditLen := func(want int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			var got int
			fixture.inspect(func(f *pluginManagerFixture) { got = len(f.audit) })
			if got != want {
				return fmt.Errorf("expected %d Plugin Manager changes, got %d", want, got)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The data source skips the purged predecessor; creating enables
				// the organization with the server defaults.
				Config: pluginEnablementConfig(server.URL, "Platform team adopts progressive rollouts", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("installation_id"), knownvalue.StringExact(pluginFixtureInstallation)),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("state"), knownvalue.StringExact("active")),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("availability"), knownvalue.StringExact("available")),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("package_sha256"), knownvalue.StringExact(strings.Repeat("ab", 32))),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("plugin_id"), knownvalue.StringExact(pluginFixturePlugin)),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("timezone"), knownvalue.StringExact("UTC")),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("approval_policies"), knownvalue.MapSizeExact(0)),
				},
				Check: func(state *terraform.State) error {
					var event pluginAuditEvent
					fixture.inspect(func(f *pluginManagerFixture) { require.Len(t, f.audit, 1); event = f.audit[0] })
					want := pluginAuditEvent{Revision: 8, OrgID: pluginFixtureOrg, State: "enabled", Timezone: "UTC", ApprovalPolicies: map[string]string{}, Reason: "Platform team adopts progressive rollouts"}
					if fmt.Sprint(event) != fmt.Sprint(want) {
						return fmt.Errorf("expected change %+v, got %+v", want, event)
					}
					return nil
				},
			},
			{
				// Only the reason changes: state records it, the Plugin Manager
				// sees no write.
				Config: pluginEnablementConfig(server.URL, "Progressive rollouts for every team", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("reason"), knownvalue.StringExact("Progressive rollouts for every team")),
				},
				Check: resource.ComposeTestCheckFunc(auditLen(1), func(*terraform.State) error {
					var puts int
					fixture.inspect(func(f *pluginManagerFixture) { puts = f.puts })
					if puts != 1 {
						return fmt.Errorf("a reason-only change must not write, got %d writes", puts)
					}
					return nil
				}),
			},
			{
				// Two other changes land between read and write: the provider
				// decides again on a fresh read each time.
				PreConfig: func() { fixture.inspect(func(f *pluginManagerFixture) { f.foreignWrites = 2 }) },
				Config:    pluginEnablementConfig(server.URL, "Progressive rollouts for every team", settings),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("timezone"), knownvalue.StringExact("Europe/Berlin")),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("approval_policies"), knownvalue.MapExact(map[string]knownvalue.Check{"environment": knownvalue.StringExact("rollouts.approve")})),
				},
				Check: resource.ComposeTestCheckFunc(auditLen(2), func(*terraform.State) error {
					var conflicts int
					var event pluginAuditEvent
					fixture.inspect(func(f *pluginManagerFixture) { conflicts, event = f.conflicts, f.audit[1] })
					if conflicts != 2 || event.Revision != 11 || event.Timezone != "Europe/Berlin" || event.ApprovalPolicies["environment"] != "rollouts.approve" {
						return fmt.Errorf("expected the third attempt to write revision 11 after 2 conflicts, got %d conflicts and %+v", conflicts, event)
					}
					return nil
				}),
			},
			{
				ResourceName:                         address,
				ImportState:                          true,
				ImportStateId:                        pluginFixtureInstallation,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "installation_id",
				ImportStateVerifyIgnore:              []string{"reason"},
			},
			{
				ResourceName:  address,
				ImportState:   true,
				ImportStateId: "not-a-uuid",
				ExpectError:   regexp.MustCompile(`Import the enablement by its installation UUID`),
			},
			{
				// An operator disabled the organization outside Terraform: the
				// next apply enables it again with its settings.
				PreConfig: func() {
					fixture.change(func(installation *cp.PluginInstallation) {
						installation.Organizations[0].State = cp.PluginEnablementStateDisabled
					})
				},
				Config: pluginEnablementConfig(server.URL, "Progressive rollouts for every team", settings),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(address, plancheck.ResourceActionCreate)},
				},
				Check: auditLen(3),
			},
			{
				// Uninstalled and reinstalled: the enablement is gone and the
				// installation waits for activation.
				PreConfig: func() {
					fixture.change(func(installation *cp.PluginInstallation) {
						installation.State, installation.Availability, installation.Organizations = cp.PluginInstallationStateInstalled, cp.PluginAvailabilityStateUnavailable, []cp.PluginEnablement{}
						installation.Diagnostic = &cp.PluginInstallationDiagnostic{Code: cp.PluginDiagnosticStatusUnreachable, Message: "Core could not read the plugin status."}
					})
				},
				Config: pluginEnablementConfig(server.URL, "Progressive rollouts for every team", settings),
				ExpectError: regexp.MustCompile(`(?s)Plugin installation not active.*\(plugin_installation_state\).*is\s+installed.*octl\s+plugin-manager\s+activate\s+` +
					pluginFixtureInstallation + `.*\(plugin_status_unreachable\):\s+Core\s+could\s+not\s+read`),
			},
			{
				// Another organization's enablement no longer blocks this one:
				// an installation serves every organization it is enabled for.
				PreConfig: func() {
					fixture.change(func(installation *cp.PluginInstallation) {
						installation.State, installation.Availability, installation.Diagnostic = cp.PluginInstallationStateActive, cp.PluginAvailabilityStateAvailable, nil
						installation.Organizations = []cp.PluginEnablement{{OrgId: "other-org", State: cp.PluginEnablementStateEnabled, Timezone: "Europe/Paris", ApprovalPolicies: map[string]string{}}}
					})
				},
				Config: pluginEnablementConfig(server.URL, "Progressive rollouts for every team", settings),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("organizations"), knownvalue.ListSizeExact(1)),
				},
				Check: func(*terraform.State) error {
					var organizations []string
					fixture.inspect(func(f *pluginManagerFixture) {
						for _, enablement := range f.installation.Organizations {
							organizations = append(organizations, enablement.OrgId+":"+string(enablement.State)+":"+enablement.Timezone)
						}
					})
					if fmt.Sprint(organizations) != "[other-org:enabled:Europe/Paris local-test-org:enabled:Europe/Berlin]" {
						return fmt.Errorf("expected both organizations enabled, each with its own settings, got %v", organizations)
					}
					return nil
				},
			},
			{
				// The limit of the installation-wide grant refuses a further
				// organization, and the diagnostic names the remedy.
				PreConfig: func() {
					fixture.change(func(installation *cp.PluginInstallation) {
						installation.Organizations = []cp.PluginEnablement{{OrgId: "other-org", State: cp.PluginEnablementStateEnabled, Timezone: "UTC", ApprovalPolicies: map[string]string{}}}
						installation.Entitlement = &cp.PluginInstallationEntitlement{EnabledOrganizations: 1, InstallationGrant: &cp.PluginEntitlementGrant{Scope: cp.PluginGrantScopeInstallation, OrganizationLimit: ref.Ref(int64(1))}}
					})
					fixture.inspect(func(f *pluginManagerFixture) { f.organizationLimit = 1 })
				},
				Config:      pluginEnablementConfig(server.URL, "Progressive rollouts for every team", settings),
				ExpectError: regexp.MustCompile(`(?s)Plugin organization limit reached.*\(plugin_organization_limit\).*has\s+1\s+enabled\s+organizations\s+and\s+its\s+installation-wide\s+licence\s+grant\s+allows\s+1.*octl\s+plugin-manager\s+import-entitlements`),
			},
			{
				// The installation is disabled for every organization: enabling
				// the organization works and warns that the plugin stays
				// safety-only.
				PreConfig: func() {
					fixture.change(func(installation *cp.PluginInstallation) {
						installation.State, installation.Availability, installation.Organizations = cp.PluginInstallationStateDisabled, cp.PluginAvailabilityStateNotEnabled, []cp.PluginEnablement{}
						installation.Entitlement = nil
					})
					fixture.inspect(func(f *pluginManagerFixture) { f.organizationLimit = 0 })
				},
				Config: pluginEnablementConfig(server.URL, "Progressive rollouts for every team", settings),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("availability"), knownvalue.StringExact("not_enabled")),
				},
				Check: auditLen(5),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			var enablement *cp.PluginEnablement
			var last pluginAuditEvent
			fixture.inspect(func(f *pluginManagerFixture) {
				enablement, last = enablementOf(f.installation, pluginFixtureOrg), f.audit[len(f.audit)-1]
			})
			if enablement == nil || enablement.State != cp.PluginEnablementStateDisabled || enablement.Timezone != "Europe/Berlin" || enablement.ApprovalPolicies["environment"] != "rollouts.approve" {
				return fmt.Errorf("destroy must disable the organization and keep its settings, got %+v", enablement)
			}
			if last.State != "disabled" || last.Reason != "Progressive rollouts for every team" {
				return fmt.Errorf("destroy must record the configured reason, got %+v", last)
			}
			return nil
		},
	})
}

// TestAccPluginEnablementInModeAllLocal covers an installation that serves
// all organizations: Core created the enablement, so creating the resource
// adopts it without a write, configured settings replace the defaults, the
// data source shows the mode, the organizations, the pending organizations
// and the licence, and destroying disables the organization (its opt-out)
// instead of removing the enablement, which Core refuses in that mode.
func TestAccPluginEnablementInModeAllLocal(t *testing.T) {
	fixture, server := newPluginManagerFixture(t)
	const address = "platform-orchestrator_plugin_enablement.rollouts"
	const lookup = "data.platform-orchestrator_plugin_installation.rollouts"
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	grant := &cp.PluginEntitlementGrant{Scope: cp.PluginGrantScopeInstallation, State: cp.PluginEntitlementGrantStateValid, TrustClass: cp.PluginEntitlementGrantTrustClassProduction,
		Issuer: "stellwerk", Sequence: 2, Features: []string{"progressive-rollouts"}, OrganizationLimit: ref.Ref(int64(5)), NotBefore: expires.AddDate(-1, 0, 0), ExpiresAt: expires, GraceUntil: expires.AddDate(0, 0, 14)}
	organization := func(path ...string) tfjsonpath.Path {
		return tfjsonpath.New("organizations").AtSliceIndex(0).AtMapKey(path[0])
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Core enabled the organization with the defaults and left
				// another one disabled as its opt-out; a third is pending.
				PreConfig: func() {
					fixture.change(func(installation *cp.PluginInstallation) {
						installation.OrganizationMode = cp.PluginOrganizationModeAll
						installation.OrganizationDefaults = cp.PluginOrganizationDefaults{Timezone: "Europe/Berlin", ApprovalPolicies: map[string]string{"environment": "rollouts.approve"}}
						installation.PendingOrganizations = &[]string{"late-org"}
						installation.Organizations = []cp.PluginEnablement{
							{OrgId: pluginFixtureOrg, State: cp.PluginEnablementStateEnabled, Timezone: "Europe/Berlin", ApprovalPolicies: map[string]string{"environment": "rollouts.approve"},
								CapabilityRegistration: cp.PluginCapabilityRegistration{State: cp.PluginCapabilityRegistrationStatePending}, EntitlementGrant: grant},
							{OrgId: "other-org", State: cp.PluginEnablementStateDisabled, Timezone: "UTC", ApprovalPolicies: map[string]string{},
								CapabilityRegistration: cp.PluginCapabilityRegistration{State: cp.PluginCapabilityRegistrationStateRegistered}},
						}
						installation.Entitlement = &cp.PluginInstallationEntitlement{EnabledOrganizations: 1, InstallationGrant: grant}
					})
				},
				Config: pluginEnablementConfig(server.URL, "Platform team keeps progressive rollouts", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("organization_mode"), knownvalue.StringExact("all")),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("default_timezone"), knownvalue.StringExact("Europe/Berlin")),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("default_approval_policies"), knownvalue.MapExact(map[string]knownvalue.Check{"environment": knownvalue.StringExact("rollouts.approve")})),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("pending_organizations"), knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("late-org")})),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("organizations"), knownvalue.ListSizeExact(2)),
					statecheck.ExpectKnownValue(lookup, organization("org_id"), knownvalue.StringExact(pluginFixtureOrg)),
					statecheck.ExpectKnownValue(lookup, organization("state"), knownvalue.StringExact("enabled")),
					statecheck.ExpectKnownValue(lookup, organization("capability_registration"), knownvalue.StringExact("pending")),
					statecheck.ExpectKnownValue(lookup, organization("licence_grant").AtMapKey("scope"), knownvalue.StringExact("installation")),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("organizations").AtSliceIndex(1).AtMapKey("state"), knownvalue.StringExact("disabled")),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("organizations").AtSliceIndex(1).AtMapKey("licence_grant"), knownvalue.Null()),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("licence").AtMapKey("enabled_organizations"), knownvalue.Int64Exact(1)),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("licence").AtMapKey("installation_grant").AtMapKey("organization_limit"), knownvalue.Int64Exact(5)),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("licence").AtMapKey("installation_grant").AtMapKey("expires_at"), knownvalue.StringExact("2027-01-02T03:04:05Z")),
					statecheck.ExpectKnownValue(lookup, tfjsonpath.New("licence").AtMapKey("installation_grant").AtMapKey("features"), knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("progressive-rollouts")})),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("timezone"), knownvalue.StringExact("Europe/Berlin")),
				},
				Check: func(*terraform.State) error {
					var puts, changes int
					fixture.inspect(func(f *pluginManagerFixture) { puts, changes = f.puts, len(f.audit) })
					if puts != 0 || changes != 0 {
						return fmt.Errorf("adopting the enablement Core created must not write, got %d writes and %d changes", puts, changes)
					}
					return nil
				},
			},
			{
				// Configured settings replace the defaults of the adopted enablement.
				Config: pluginEnablementConfig(server.URL, "Platform team keeps progressive rollouts", `  timezone = "Asia/Tokyo"`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("timezone"), knownvalue.StringExact("Asia/Tokyo")),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("approval_policies"), knownvalue.MapExact(map[string]knownvalue.Check{"environment": knownvalue.StringExact("rollouts.approve")})),
				},
				Check: func(*terraform.State) error {
					var changes []pluginAuditEvent
					fixture.inspect(func(f *pluginManagerFixture) { changes = f.audit })
					if len(changes) != 1 || changes[0].State != "enabled" || changes[0].Timezone != "Asia/Tokyo" || changes[0].ApprovalPolicies["environment"] != "rollouts.approve" {
						return fmt.Errorf("expected one change of the timezone that keeps the policies, got %+v", changes)
					}
					return nil
				},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			var enablement *cp.PluginEnablement
			var mode cp.PluginOrganizationMode
			var last pluginAuditEvent
			fixture.inspect(func(f *pluginManagerFixture) {
				enablement, mode, last = enablementOf(f.installation, pluginFixtureOrg), f.installation.OrganizationMode, f.audit[len(f.audit)-1]
			})
			if enablement == nil || enablement.State != cp.PluginEnablementStateDisabled || enablement.Timezone != "Asia/Tokyo" {
				return fmt.Errorf("destroy must keep the enablement, disabled, got %+v", enablement)
			}
			if mode != cp.PluginOrganizationModeAll || last.State != "disabled" {
				return fmt.Errorf("destroy must be the organization's opt-out in mode all, got mode %s and %+v", mode, last)
			}
			return nil
		},
	})
}
