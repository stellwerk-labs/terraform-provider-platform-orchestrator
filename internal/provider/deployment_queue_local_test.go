package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/ref"
)

const (
	queueFixtureOrg   = "local-test-org"
	queueFixtureToken = "local-test-token"
	queueFixtureItem  = "33333333-3333-4333-8333-333333333333"
	fencingDeployment = "44444444-4444-4444-8444-444444444444"
	failedDeployment  = "55555555-5555-4555-8555-555555555555"
)

// deploymentQueueFixture is a Data Plane whose Environment is always busy:
// another Deployment runs (running) or requests wait in its queue. Like the
// real Data Plane, it queues a deploy without when_busy or with queue (202),
// answers one with reject with 409 environment_busy, and promotes the queue
// item after the reads configured in queueReads. A plan_only request is never
// queued and ignores when_busy: it gets 409 environment_busy while another
// Deployment runs and starts while requests only wait. While fence is set, it
// refuses every deploy with 409 outcome_unreconciled and reports the fence on
// the Environment's last deployment. A plan_only Deployment carries
// pending_changes while requests wait. A queued request with halt_on_failure is
// rejected with previous_deployment_failed instead of being promoted while
// failedPredecessor names the failed Deployment it waited for, which the item
// names in reason_deployment_id and in its message; halt_on_failure or
// queue_ttl_seconds with when_busy reject is a 400.
type deploymentQueueFixture struct {
	t  *testing.T
	mu sync.Mutex

	// running says another deploy, rollback or destroy runs, not only queued
	// requests wait. waiting is the number of requests waiting in the queue.
	running bool
	waiting int

	// queueReads are the states successive reads of the queue item return;
	// the last one repeats. dispatched is the Deployment the item became.
	queueReads []dp.DeploymentQueueItemState
	position   int
	reason     string
	dispatched *dp.Deployment

	// cancelLoses makes a cancellation lose the race against promotion.
	cancelLoses bool

	// halt is the halt_on_failure of the last request. omitReasonDeployment
	// leaves reason_deployment_id out of a halted item, so that only its
	// message names the failed Deployment.
	halt                 bool
	failedPredecessor    string
	omitReasonDeployment bool

	fence           *dp.EnvironmentFence
	fenceUnreadable bool

	deployments map[uuid.UUID]*dp.Deployment
	recipient   string
	creates     []dp.DeploymentCreateBody
	reads       int
	cancels     []string
}

func newDeploymentQueueFixture(t *testing.T) (*deploymentQueueFixture, *httptest.Server) {
	t.Helper()
	fixture := &deploymentQueueFixture{t: t, running: true, deployments: map[uuid.UUID]*dp.Deployment{}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	previous := deploymentQueuePollInterval
	deploymentQueuePollInterval = 20 * time.Millisecond
	t.Cleanup(func() { deploymentQueuePollInterval = previous })
	return fixture, server
}

func (f *deploymentQueueFixture) configure(change func(*deploymentQueueFixture)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *deploymentQueueFixture) item(state dp.DeploymentQueueItemState) dp.DeploymentQueueItem {
	item := dp.DeploymentQueueItem{
		Id: uuid.MustParse(queueFixtureItem), OrgId: queueFixtureOrg, ProjectId: "shop", EnvId: "production",
		Mode: dp.DeploymentQueueItemModeDeploy, State: state, HaltOnFailure: f.halt, CreatedBy: uuid.New(), CreatedAt: time.Now(), ExpiresAt: time.Now().Add(2 * time.Hour),
	}
	switch state {
	case dp.Queued:
		item.Position = f.position
	case dp.Dispatched:
		if f.dispatched == nil {
			f.dispatched = f.start()
		}
		item.DeploymentId = &f.dispatched.Id
	case dp.Rejected:
		code, message := f.reason, f.reason
		if f.halts() {
			code, message = "previous_deployment_failed", fmt.Sprintf("the previous Deployment '%s' failed", f.failedPredecessor)
			if !f.omitReasonDeployment {
				item.ReasonDeploymentId = ref.Ref(uuid.MustParse(f.failedPredecessor))
			}
		}
		item.ReasonCode, item.ReasonMessage = &code, &message
	default:
		item.ReasonCode = &f.reason
		item.ReasonMessage = &f.reason
	}
	return item
}

// halts says whether promotion rejects the queued request: it asked to halt and
// the Deployment it waited for failed.
func (f *deploymentQueueFixture) halts() bool {
	return f.halt && f.failedPredecessor != ""
}

// start creates the Deployment a request becomes, already executing.
func (f *deploymentQueueFixture) start() *dp.Deployment {
	deployment := &dp.Deployment{Id: uuid.New(), OrgId: queueFixtureOrg, ProjectId: "shop", EnvId: "production", Mode: "deploy", Status: "executing", CreatedAt: time.Now().UTC().Truncate(time.Second), RunnerId: "runner"}
	f.deployments[deployment.Id] = deployment
	return deployment
}

func (f *deploymentQueueFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		assert.NoError(f.t, json.NewEncoder(w).Encode(value))
	}
	refuse := func(status int, code, message string) {
		write(status, map[string]string{"error": code, "message": message})
	}
	if r.Header.Get("Authorization") != "Bearer "+queueFixtureToken {
		refuse(http.StatusUnauthorized, "API-401", "unauthorized")
		return
	}
	org := "/orgs/" + queueFixtureOrg
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == org+"/deployments":
		var body dp.DeploymentCreateBody
		if !assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&body)) {
			refuse(http.StatusBadRequest, "API-400", "invalid body")
			return
		}
		f.creates = append(f.creates, body)
		f.recipient = *body.EncryptedOutputsRecipient
		f.halt = body.HaltOnFailure != nil && *body.HaltOnFailure
		reject := body.WhenBusy != nil && *body.WhenBusy == dp.Reject
		switch {
		case body.Mode != dp.DeploymentCreateBodyModePlanOnly && reject && (f.halt || body.QueueTtlSeconds != nil):
			refuse(http.StatusBadRequest, "API-400", "queue_ttl_seconds and halt_on_failure are not valid with when_busy reject")
		case body.Mode == dp.DeploymentCreateBodyModePlanOnly && f.running:
			refuse(http.StatusConflict, "environment_busy", "deployment is still executing")
		case body.Mode == dp.DeploymentCreateBodyModePlanOnly:
			planned := *f.start()
			if f.waiting > 0 {
				planned.PendingChanges = &dp.PendingEnvironmentChanges{Code: dp.ResultMayChange, Queued: f.waiting}
			}
			write(http.StatusCreated, planned)
		case f.fence != nil:
			refuse(http.StatusConflict, "outcome_unreconciled", fmt.Sprintf("failed plugin-dispatched Deployment '%s' may still have an active Runner; outcome is unreconciled", f.fence.DeploymentId))
		case reject:
			refuse(http.StatusConflict, "environment_busy", "deployment is still executing")
		default:
			f.position, f.dispatched = 2, nil
			write(http.StatusAccepted, f.item(dp.Queued))
		}
	case r.Method == http.MethodGet && path == org+"/last-deployments":
		assert.Equal(f.t, url.Values{"project_id": {"shop"}, "env_id": {"production"}}, r.URL.Query())
		if f.fenceUnreadable {
			refuse(http.StatusInternalServerError, "API-500", "internal error")
			return
		}
		head := dp.DeploymentSummary{Id: uuid.New(), OrgId: queueFixtureOrg, ProjectId: "shop", EnvId: "production", Mode: "deploy", Status: "failed", EnvironmentFence: f.fence}
		write(http.StatusOK, dp.DeploymentPage{Items: []dp.DeploymentSummary{head}})
	case r.Method == http.MethodGet && path == org+"/deployment-queue/"+queueFixtureItem:
		f.reads++
		state := f.queueReads[0]
		if len(f.queueReads) > 1 {
			f.queueReads = f.queueReads[1:]
		}
		if state == dp.Queued && f.position > 1 {
			f.position--
		}
		if state == dp.Dispatched && f.halts() {
			state = dp.Rejected
		}
		write(http.StatusOK, f.item(state))
	case r.Method == http.MethodPost && path == org+"/deployment-queue/"+queueFixtureItem+"/actions/cancel":
		var body dp.DeploymentQueueCancelBody
		assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&body))
		f.cancels = append(f.cancels, *body.Reason)
		if f.cancelLoses {
			f.queueReads = []dp.DeploymentQueueItemState{dp.Dispatched}
			refuse(http.StatusConflict, "queue_item_settled", "the queue item is already dispatched")
			return
		}
		f.queueReads = []dp.DeploymentQueueItemState{dp.Cancelled}
		write(http.StatusOK, f.item(dp.Cancelled))
	case r.Method == http.MethodGet && strings.HasPrefix(path, org+"/deployments/"):
		id, action, _ := strings.Cut(strings.TrimPrefix(path, org+"/deployments/"), "/")
		deployment, found := f.deployments[uuid.MustParse(id)]
		if !found {
			refuse(http.StatusNotFound, "API-404", "not found")
			return
		}
		switch action {
		case "":
			write(http.StatusOK, deployment)
		case "actions/wait-for-complete":
			deployment.Status, deployment.CompletedAt = "succeeded", ref.Ref(deployment.CreatedAt.Add(time.Minute))
			write(http.StatusOK, deployment)
		case "encrypted-outputs":
			write(http.StatusOK, dp.DeploymentEncryptedOutputs{Raw: f.encryptedOutputs()})
		default:
			refuse(http.StatusNotFound, "API-404", "not found")
		}
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, path)
		refuse(http.StatusNotFound, "API-404", "not found")
	}
}

func (f *deploymentQueueFixture) encryptedOutputs() string {
	recipient, err := age.ParseX25519Recipient(f.recipient)
	require.NoError(f.t, err)
	var encoded bytes.Buffer
	encoder := base64.NewEncoder(base64.StdEncoding, &encoded)
	writer, err := age.Encrypt(encoder, recipient)
	require.NoError(f.t, err)
	_, err = writer.Write([]byte(`{"url":"https://shop.example.com"}`))
	require.NoError(f.t, err)
	require.NoError(f.t, writer.Close())
	require.NoError(f.t, encoder.Close())
	return encoded.String()
}

func (f *deploymentQueueFixture) lastCreate() dp.DeploymentCreateBody {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates[len(f.creates)-1]
}

// counts returns how many deployment requests and queue item reads arrived.
func (f *deploymentQueueFixture) counts() (creates, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates), f.reads
}

func (f *deploymentQueueFixture) cancelReasons() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cancels)
}

func deploymentQueueConfig(apiURL, workload, extra string) string {
	return fmt.Sprintf(`
provider "platform-orchestrator" {
  api_url    = %[1]q
  org_id     = %[2]q
  auth_token = %[3]q
}

resource "platform-orchestrator_deployment" "shop" {
  project_id = "shop"
  env_id     = "production"
  manifest   = jsonencode({ workloads = { %[4]s = {} } })
%[5]s
}
`, apiURL, queueFixtureOrg, queueFixtureToken, workload, extra)
}

// TestAccDeploymentQueueLocal is the provider's deployment queue acceptance:
// it waits through the Environment's deployment queue by default,
// within the create or update timeout, and reports every way out of the queue
// as a diagnostic with its stable code.
func TestAccDeploymentQueueLocal(t *testing.T) {
	fixture, server := newDeploymentQueueFixture(t)
	const address = "platform-orchestrator_deployment.shop"
	var firstID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Default when_busy: the request waits at positions 2 and 1,
				// starts, completes and its outputs are decrypted.
				PreConfig: func() {
					fixture.configure(func(f *deploymentQueueFixture) {
						f.queueReads = []dp.DeploymentQueueItemState{dp.Queued, dp.Queued, dp.Queued, dp.Dispatched}
					})
				},
				Config: deploymentQueueConfig(server.URL, "web", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("status"), knownvalue.StringExact("succeeded")),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("outputs"), knownvalue.StringExact(`{"url":"https://shop.example.com"}`)),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("when_busy"), knownvalue.Null()),
					statecheck.ExpectKnownValue(address, tfjsonpath.New("halt_on_failure"), knownvalue.Null()),
				},
				Check: func(state *terraform.State) error {
					firstID = state.RootModule().Resources[address].Primary.ID
					create := fixture.lastCreate()
					if create.WhenBusy == nil || *create.WhenBusy != dp.Queue {
						return fmt.Errorf("the provider must queue by default, sent when_busy %v", create.WhenBusy)
					}
					if creates, reads := fixture.counts(); creates != 1 || reads != 4 {
						return fmt.Errorf("expected one request followed through 4 queue reads, got %d creates and %d reads", creates, reads)
					}
					return nil
				},
			},
			{
				// Only how to deploy changes: no redeployment.
				Config: deploymentQueueConfig(server.URL, "web", `  when_busy = "queue"
  timeouts {
    create = "30m"
    update = "45m"
  }`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("when_busy"), knownvalue.StringExact("queue")),
				},
				Check: func(state *terraform.State) error {
					if creates, _ := fixture.counts(); state.RootModule().Resources[address].Primary.ID != firstID || creates != 1 {
						return fmt.Errorf("changing when_busy or timeouts must not redeploy, got %d creates", creates)
					}
					return nil
				},
			},
			{
				// halt_on_failure is how to deploy as well: no redeployment.
				Config: deploymentQueueConfig(server.URL, "web", `  when_busy       = "queue"
  halt_on_failure = true
  timeouts {
    create = "30m"
    update = "45m"
  }`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("halt_on_failure"), knownvalue.Bool(true)),
				},
				Check: func(state *terraform.State) error {
					if creates, _ := fixture.counts(); state.RootModule().Resources[address].Primary.ID != firstID || creates != 1 {
						return fmt.Errorf("changing halt_on_failure must not redeploy, got %d creates", creates)
					}
					return nil
				},
			},
			{
				Config:      deploymentQueueConfig(server.URL, "api", `  when_busy = "reject"`),
				ExpectError: regexp.MustCompile(`(?s)Environment busy.*environment_busy.*when_busy\s+=\s+"queue"`),
			},
			{
				// Refused when planning: only a request that may wait can halt.
				Config: deploymentQueueConfig(server.URL, "api", `  when_busy       = "reject"
  halt_on_failure = true`),
				ExpectError: regexp.MustCompile(`(?s)Invalid halt_on_failure.*not\s+valid\s+with\s+when_busy\s+=\s+"reject"`),
			},
			{
				// The deployment the request waited for failed: the request is
				// rejected and the diagnostic names that deployment.
				PreConfig: func() {
					fixture.configure(func(f *deploymentQueueFixture) {
						f.queueReads, f.failedPredecessor = []dp.DeploymentQueueItemState{dp.Queued, dp.Dispatched}, failedDeployment
					})
				},
				Config: deploymentQueueConfig(server.URL, "api", `  halt_on_failure = true`),
				ExpectError: regexp.MustCompile(`(?s)Deployment halted after a failure.*ended\s+rejected\s+\(previous_deployment_failed\).*did\s+not\s+start\s+after\s+deployment\s+` +
					failedDeployment + `\s+failed`),
			},
			{
				PreConfig: func() {
					fixture.configure(func(f *deploymentQueueFixture) {
						f.queueReads, f.reason = []dp.DeploymentQueueItemState{dp.Queued, dp.Rejected}, "pin_protection"
					})
				},
				Config:      deploymentQueueConfig(server.URL, "api", ""),
				ExpectError: regexp.MustCompile(`(?s)Queued deployment did not start.*ended\s+rejected\s+\(pin_protection\)`),
			},
			{
				// A fenced Environment refuses the deploy; the diagnostic names
				// the fencing deployment and the action that lifts the fence.
				PreConfig: func() {
					fixture.configure(func(f *deploymentQueueFixture) {
						f.fence = &dp.EnvironmentFence{Code: dp.OutcomeUnreconciled, DeploymentId: uuid.MustParse(fencingDeployment), Remedy: dp.AcknowledgeOutcome,
							Since: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC), RemedyAvailableAt: ref.Ref(time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC))}
					})
				},
				Config: deploymentQueueConfig(server.URL, "api", ""),
				ExpectError: regexp.MustCompile(`(?s)Deployment outcome unreconciled.*\(outcome_unreconciled\).*fenced\s+since\s+2026-10-07T09:00:00Z\s+by\s+failed\s+plugin-dispatched\s+deployment\s+` +
					fencingDeployment + `.*/actions/acknowledge-outcome\),\s+accepted\s+from\s+2026-10-07T10:00:00Z`),
			},
			{
				// Acknowledging the outcome lifts the fence and rejects what
				// waited in the queue: the request is to be applied again.
				PreConfig: func() {
					fixture.configure(func(f *deploymentQueueFixture) {
						f.fence, f.queueReads, f.reason = nil, []dp.DeploymentQueueItemState{dp.Queued, dp.Rejected}, "outcome_unreconciled"
					})
				},
				Config:      deploymentQueueConfig(server.URL, "api", ""),
				ExpectError: regexp.MustCompile(`(?s)Queued deployment did not start.*\(outcome_unreconciled\).*no\s+longer\s+fenced.*Apply\s+again`),
			},
			{
				// The update timeout covers the queue: a request still waiting
				// when it elapses is cancelled, so nothing untracked starts later.
				PreConfig: func() {
					fixture.configure(func(f *deploymentQueueFixture) { f.queueReads = []dp.DeploymentQueueItemState{dp.Queued} })
				},
				Config: deploymentQueueConfig(server.URL, "api", `  timeouts {
    update = "1s"
  }`),
				ExpectError: regexp.MustCompile(`(?s)Timed out in the deployment queue.*was\s+cancelled\s+\(queue\s+item\s+` + queueFixtureItem + `\)`),
			},
			{
				// plan_only is never queued and does not run beside another
				// deployment of the environment, whatever when_busy says.
				Config: deploymentQueueConfig(server.URL, "api", `  mode      = "plan_only"
  wait_for  = false
  when_busy = "queue"`),
				ExpectError: regexp.MustCompile(`(?s)Environment busy.*\(environment_busy\).*plan_only\s+deployments\s+never\s+wait`),
			},
			{
				// While requests only wait, plan_only starts; when_busy is not sent.
				PreConfig: func() {
					fixture.configure(func(f *deploymentQueueFixture) { f.running, f.waiting = false, 2 })
				},
				Config: deploymentQueueConfig(server.URL, "api", `  mode     = "plan_only"
  wait_for = false`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(address, tfjsonpath.New("mode"), knownvalue.StringExact("plan_only")),
				},
				Check: func(*terraform.State) error {
					if create := fixture.lastCreate(); create.WhenBusy != nil {
						return fmt.Errorf("plan_only must not send when_busy, sent %s", *create.WhenBusy)
					}
					return nil
				},
			},
		},
	})
	cancels := fixture.cancelReasons()
	require.Len(t, cancels, 1, "exactly the timed-out request is cancelled")
	require.Contains(t, cancels[0], "timeout elapsed")
}
