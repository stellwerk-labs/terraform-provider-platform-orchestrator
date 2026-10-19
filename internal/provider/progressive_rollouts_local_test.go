package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func rolloutsFixtureProviderConfig(apiURL string) string {
	return fmt.Sprintf(`
provider "platform-orchestrator" {
  api_url    = %q
  org_id     = %q
  auth_token = %q
}
`, apiURL, rolloutsFixtureOrg, rolloutsFixtureToken)
}

func progressiveRolloutsLocalConfig(apiURL, desiredState, name string) string {
	return rolloutsFixtureProviderConfig(apiURL) + fmt.Sprintf(`
resource "platform-orchestrator_progressive_rollout" "payments" {
  idempotency_key = "terraform-local-payments"
  desired_state   = %[1]q
  control_reason  = "Local acceptance"
  spec = jsonencode({
    name              = %[2]q
    default_promotion = "none"
    changes = [{
      module_id      = "payments"
      from           = "1.0.0"
      target_version = "1.1.0"
      target_digest  = ""
      target_status  = "default"
    }]
    selector = { environment_ids = ["11111111-1111-4111-8111-111111111111"] }
    waves = [{
      id                = "canary"
      name              = "Canary"
      environment_ids   = ["11111111-1111-4111-8111-111111111111"]
      approval          = "none"
      progression       = "automatic"
      bake_time_seconds = 0
      retry = {
        max_attempts             = 1
        initial_delay_seconds    = 1
        maximum_delay_seconds    = 1
        maximum_duration_seconds = 60
      }
      max_concurrency = { absolute = 1 }
    }]
  })
}

data "platform-orchestrator_progressive_rollout" "payments" {
  id = platform-orchestrator_progressive_rollout.payments.id
}
`, desiredState, name)
}

func rolloutStateChecks(state string) []statecheck.StateCheck {
	return []statecheck.StateCheck{
		statecheck.ExpectKnownValue("platform-orchestrator_progressive_rollout.payments", tfjsonpath.New("state"), knownvalue.StringExact(state)),
		statecheck.ExpectKnownValue("data.platform-orchestrator_progressive_rollout.payments", tfjsonpath.New("state"), knownvalue.StringExact(state)),
	}
}

// TestAccProgressiveRolloutsLocal drives real Terraform through the native
// plugin path against an in-process Core gateway and plugin, without any
// network dependency.
func TestAccProgressiveRolloutsLocal(t *testing.T) {
	fixture, server := newRolloutsCoreFixture(t)
	config := func(desiredState, name string) string {
		return progressiveRolloutsLocalConfig(server.URL, desiredState, name)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("draft", "payments"),
				ConfigStateChecks: append(rolloutStateChecks("Draft"),
					statecheck.ExpectKnownValue("platform-orchestrator_progressive_rollout.payments", tfjsonpath.New("resource_version"), knownvalue.Int64Exact(1)),
					statecheck.ExpectKnownValue("platform-orchestrator_progressive_rollout.payments", tfjsonpath.New("current_plan_revision"), knownvalue.Null()),
				),
			},
			{
				// Licence in grace: forward work still succeeds; Core's warning
				// is a warning diagnostic, never an error.
				PreConfig: func() { fixture.enterGrace("2026-11-01T00:00:00Z") },
				Config:    config("running", "payments"),
				ConfigStateChecks: append(rolloutStateChecks("Running"),
					statecheck.ExpectKnownValue("platform-orchestrator_progressive_rollout.payments", tfjsonpath.New("current_plan_revision"), knownvalue.Int64Exact(1)),
					statecheck.ExpectKnownValue("platform-orchestrator_progressive_rollout.payments", tfjsonpath.New("plan_json"), knownvalue.NotNull()),
				),
			},
			{
				Config:                  config("running", "payments"),
				ResourceName:            "platform-orchestrator_progressive_rollout.payments",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"idempotency_key", "control_reason", "spec"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 || states[0].Attributes["desired_state"] != "running" {
						return fmt.Errorf("import must adopt desired_state running, got %v", states)
					}
					var spec struct {
						Name string `json:"name"`
					}
					if err := json.Unmarshal([]byte(states[0].Attributes["spec"]), &spec); err != nil || spec.Name != "payments" {
						return fmt.Errorf("import must reconstruct the server spec, got %q", states[0].Attributes["spec"])
					}
					return nil
				},
			},
			{
				// Licence expired: refresh, resource reads and the data source keep
				// working and the unchanged configuration plans nothing.
				PreConfig: fixture.expireLicence,
				Config:    config("running", "payments"),
				PlanOnly:  true,
			},
			{
				Config:      config("running", "payments-v2"),
				ExpectError: regexp.MustCompile(`plugin_not_entitled`),
			},
			{
				// Pausing is a safety command and stays available.
				Config:            config("paused", "payments"),
				ConfigStateChecks: rolloutStateChecks("Paused"),
			},
			{
				// Resuming is forward work; Core refuses it with the stable code.
				Config:      config("running", "payments"),
				ExpectError: regexp.MustCompile(`plugin_not_entitled`),
			},
			{
				Config:            config("rolled_back", "payments"),
				ConfigStateChecks: rolloutStateChecks("RolledBack"),
				Check: func(*terraform.State) error {
					invoked := fixture.operationsInvoked()
					if slices.Contains(invoked, "updateProgressiveRolloutDraft") {
						return fmt.Errorf("an expired licence must stop the Draft update before it reaches Core: %v", invoked)
					}
					for _, required := range []string{"createProgressiveRollout", "previewProgressiveRollout", "prepareProgressiveRollout", "commandProgressiveRollout:start", "commandProgressiveRollout:pause", "commandProgressiveRollout:resume", "commandProgressiveRollout:cancel-and-rollback"} {
						if !slices.Contains(invoked, required) {
							return fmt.Errorf("expected invocation %s, got %v", required, invoked)
						}
					}
					return nil
				},
			},
		},
	})
}

// TestAccProgressiveRolloutsAvailabilityLocal proves every availability state,
// and a Core without plugin discovery, stops plugin work with its stable code.
func TestAccProgressiveRolloutsAvailabilityLocal(t *testing.T) {
	fixture, server := newRolloutsCoreFixture(t)
	step := func(code string, change func(*rolloutsCoreFixture)) resource.TestStep {
		return resource.TestStep{
			PreConfig:   func() { fixture.configure(change) },
			Config:      progressiveRolloutsLocalConfig(server.URL, "draft", "payments"),
			ExpectError: regexp.MustCompile(code),
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			step("plugin_not_installed", func(f *rolloutsCoreFixture) { f.capabilityStatus = http.StatusNotFound }),
			step("plugin_not_installed", func(f *rolloutsCoreFixture) {
				f.capabilityStatus, f.state, f.listed = http.StatusOK, "not_installed", false
			}),
			step("plugin_not_enabled", func(f *rolloutsCoreFixture) { f.state, f.listed, f.features = "not_enabled", true, nil }),
			step("plugin_not_entitled", func(f *rolloutsCoreFixture) {
				f.state, f.features = "not_entitled", map[string]rolloutsFeatureAccess{rolloutsFixtureFeature: {Read: true, Safety: true}}
			}),
			step("plugin_incompatible", func(f *rolloutsCoreFixture) { f.state = "incompatible" }),
			step("plugin_unavailable", func(f *rolloutsCoreFixture) { f.state = "unavailable" }),
		},
	})
	if invoked := fixture.operationsInvoked(); len(invoked) != 0 {
		t.Fatalf("no plugin operation may be invoked while the plugin is unavailable, got %v", invoked)
	}
}

// expectChangedAttributes passes when the planned change of address alters
// exactly attributes (sorted) and leaves every other attribute known and equal.
type expectChangedAttributes struct {
	address    string
	attributes []string
}

func (e expectChangedAttributes) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, change := range req.Plan.ResourceChanges {
		if change.Address != e.address {
			continue
		}
		before, _ := change.Change.Before.(map[string]any)
		after, _ := change.Change.After.(map[string]any)
		unknown, _ := change.Change.AfterUnknown.(map[string]any)
		names := map[string]bool{}
		for _, values := range []map[string]any{before, after, unknown} {
			for name := range values {
				names[name] = true
			}
		}
		var changed []string
		for name := range names {
			if unknown[name] == true || !reflect.DeepEqual(before[name], after[name]) {
				changed = append(changed, name)
			}
		}
		slices.Sort(changed)
		if !slices.Equal(changed, e.attributes) {
			resp.Error = fmt.Errorf("%s: expected changes to exactly %v, got %v (unknown after apply: %v)", e.address, e.attributes, changed, unknown)
		}
		return
	}
	resp.Error = fmt.Errorf("%s: no planned change", e.address)
}

// TestAccProgressiveRolloutImportLocal imports a Rollout that Terraform does
// not track and applies its configuration. Import cannot read back the
// create-only idempotency_key or the control_reason, so the plan stores both
// and nothing else: the Rollout keeps its known id, version and state, and the
// apply reaches no plugin operation.
func TestAccProgressiveRolloutImportLocal(t *testing.T) {
	fixture, server := newRolloutsCoreFixture(t)
	const address = "platform-orchestrator_progressive_rollout.payments"
	config := progressiveRolloutsLocalConfig(server.URL, "draft", "payments")
	var rolloutID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					rolloutID = s.RootModule().Resources[address].Primary.ID
					return nil
				},
			},
			{
				// Destroy only forgets the state reference; the Draft stays.
				Config: rolloutsFixtureProviderConfig(server.URL),
			},
			{
				Config:             config,
				ResourceName:       address,
				ImportState:        true,
				ImportStatePersist: true,
				ImportStateIdFunc:  func(*terraform.State) (string, error) { return rolloutID, nil },
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected one imported Rollout, got %d", len(states))
					}
					attributes := states[0].Attributes
					if attributes["id"] != rolloutID || attributes["state"] != "Draft" || attributes["desired_state"] != "draft" || attributes["resource_version"] != "1" {
						return fmt.Errorf("import must read the Draft %s, got %v", rolloutID, attributes)
					}
					if attributes["idempotency_key"] != "" || attributes["control_reason"] != "" {
						return fmt.Errorf("import cannot know the idempotency_key or control_reason, got %v", attributes)
					}
					return nil
				},
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(address, plancheck.ResourceActionUpdate),
						expectChangedAttributes{address: address, attributes: []string{"control_reason", "idempotency_key"}},
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("idempotency_key"), knownvalue.StringExact("terraform-local-payments")),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("control_reason"), knownvalue.StringExact("Local acceptance")),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("resource_version"), knownvalue.Int64Exact(1)),
				},
				Check: func(*terraform.State) error {
					writes := slices.DeleteFunc(fixture.operationsInvoked(), func(operation string) bool { return operation == "getProgressiveRollout" })
					if !slices.Equal(writes, []string{"createProgressiveRollout"}) {
						return fmt.Errorf("storing the import's Terraform-only attributes must reach no plugin operation, got %v", writes)
					}
					return nil
				},
			},
		},
	})
}
