package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/ref"
)

func newQueueTestResource(t *testing.T) (*deploymentQueueFixture, *DeploymentResource) {
	t.Helper()
	fixture, server := newDeploymentQueueFixture(t)
	client, err := dp.NewClientWithResponses(server.URL, dp.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+queueFixtureToken)
		return nil
	}))
	require.NoError(t, err)
	return fixture, &DeploymentResource{dpClient: client, orgId: queueFixtureOrg}
}

// A cancellation after the timeout can lose against promotion. The Deployment
// that started anyway is returned so that state records it.
func TestTimedOutQueuedDeploymentThatStartedAnywayIsRecorded(t *testing.T) {
	fixture, resource := newQueueTestResource(t)
	fixture.configure(func(f *deploymentQueueFixture) { f.cancelLoses, f.position = true, 1 })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var diagnostics diag.Diagnostics
	deployment := resource.awaitQueuedDeployment(ctx, fixture.item(dp.Queued), &diagnostics)
	require.NotNil(t, deployment)
	fixture.configure(func(f *deploymentQueueFixture) { require.Equal(t, f.dispatched.Id, deployment.Id) })
	require.Len(t, diagnostics.Errors(), 1)
	require.Equal(t, "Timed out as the queued deployment started", diagnostics.Errors()[0].Summary())
	require.Contains(t, diagnostics.Errors()[0].Detail(), deployment.Id.String())
	require.Len(t, fixture.cancelReasons(), 1)
}

func TestDeploymentConflictsAreDiagnosticsWithTheirCode(t *testing.T) {
	t.Parallel()
	for code, summary := range map[string]string{
		"environment_busy":        "Environment busy",
		"deployment_queue_full":   "Deployment queue full",
		"outcome_unreconciled":    "Deployment outcome unreconciled",
		"deployment_base_changed": "Deployment base changed",
		"environment_deleting":    "Environment deleting",
		"HTTP-409":                PO_API_ERR,
	} {
		var diagnostics diag.Diagnostics
		addDeploymentConflictError(&diagnostics, ref.Ref(dp.Queue), &dp.N409Conflict{Error: code, Message: "refused"}, nil, " Fenced.")
		require.Len(t, diagnostics.Errors(), 1, code)
		require.Equal(t, summary, diagnostics.Errors()[0].Summary(), code)
		require.Contains(t, diagnostics.Errors()[0].Detail(), "("+code+"): refused.", code)
		require.Equal(t, code == "outcome_unreconciled", strings.HasSuffix(diagnostics.Errors()[0].Detail(), "refused. Fenced."), code)
	}
	var diagnostics diag.Diagnostics
	addDeploymentConflictError(&diagnostics, ref.Ref(dp.Queue), nil, []byte("busy"), "")
	require.Equal(t, "Unable to create deployment, conflict: busy", diagnostics.Errors()[0].Detail())
}

// environment_busy says why the deployment did not wait, which depends on
// what it asked for: reject, queue (only an organization token is refused
// then) or nothing, for plan_only.
func TestEnvironmentBusySaysWhyTheDeploymentDidNotWait(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sent *dp.DeploymentCreateBodyWhenBusy
		want string
	}{
		"reject":    {ref.Ref(dp.Reject), `Another deployment is running or waiting. Remove when_busy or set when_busy = "queue" (the default) to wait in the environment's deployment queue within the create or update timeout.`},
		"queue":     {ref.Ref(dp.Queue), "Requests authorized by an organization token cannot wait in the deployment queue. Configure the provider with the token of a user or service user to wait, or apply again once the environment is free."},
		"plan_only": {nil, "plan_only deployments never wait in the environment's deployment queue and do not run beside another deployment of the environment; apply again once it finished."},
	} {
		var diagnostics diag.Diagnostics
		addDeploymentConflictError(&diagnostics, tc.sent, &dp.N409Conflict{Error: "environment_busy", Message: "busy"}, nil, "")
		require.Len(t, diagnostics.Errors(), 1, name)
		require.Equal(t, "Environment busy", diagnostics.Errors()[0].Summary(), name)
		require.Equal(t, "The deployment was refused (environment_busy): busy. "+tc.want, diagnostics.Errors()[0].Detail(), name)
	}
}

// halt_on_failure travels only with a deploy that may wait in the queue.
func TestDeploymentSendsHaltOnFailureOnlyWhenItMayWait(t *testing.T) {
	for name, tc := range map[string]struct {
		mode, whenBusy string
		halt           types.Bool
		want           *bool
	}{
		"halt":                {mode: "deploy", halt: types.BoolValue(true), want: ref.Ref(true)},
		"halt with queue":     {mode: "deploy", whenBusy: whenBusyQueue, halt: types.BoolValue(true), want: ref.Ref(true)},
		"false":               {mode: "deploy", halt: types.BoolValue(false)},
		"unset":               {mode: "deploy", halt: types.BoolNull()},
		"halt with reject":    {mode: "deploy", whenBusy: whenBusyReject, halt: types.BoolValue(true)},
		"halt with plan_only": {mode: "plan_only", halt: types.BoolValue(true)},
	} {
		t.Run(name, func(t *testing.T) {
			fixture, resource := newQueueTestResource(t)
			fixture.configure(func(f *deploymentQueueFixture) {
				f.running, f.queueReads = false, []dp.DeploymentQueueItemState{dp.Dispatched}
			})
			data := DeploymentResourceModel{
				ProjectId: types.StringValue("shop"), EnvId: types.StringValue("production"), Manifest: types.StringValue(`{"workloads":{}}`),
				Mode: types.StringValue(tc.mode), WaitFor: types.BoolValue(false), WhenBusy: types.StringNull(), HaltOnFailure: tc.halt,
			}
			if tc.whenBusy != "" {
				data.WhenBusy = types.StringValue(tc.whenBusy)
			}
			var diagnostics diag.Diagnostics
			resource.doDeployment(t.Context(), &data, &diagnostics)
			require.Equal(t, tc.want, fixture.lastCreate().HaltOnFailure)
		})
	}
}

// A request that asked to halt and was rejected because the deployment it
// waited for failed gets its own diagnostic, naming that deployment from the
// item's reason_deployment_id. The reason message is quoted, never parsed.
func TestHaltedQueuedDeploymentNamesTheFailedDeployment(t *testing.T) {
	for name, tc := range map[string]struct {
		omitReasonDeployment bool
		want, unwanted       string
	}{
		"named": {want: "The deployment request waited in the environment's deployment queue as item " + queueFixtureItem +
			" and ended rejected (previous_deployment_failed): the previous Deployment '" + failedDeployment + "' failed. " +
			"halt_on_failure is set, so the deployment did not start after deployment " + failedDeployment + " failed. Find out why it failed, fix the cause and apply again."},
		"unnamed": {omitReasonDeployment: true, want: "halt_on_failure is set, so the deployment did not start after the deployment it waited for failed.",
			unwanted: "after deployment " + failedDeployment},
	} {
		t.Run(name, func(t *testing.T) {
			fixture, resource := newQueueTestResource(t)
			fixture.configure(func(f *deploymentQueueFixture) {
				f.halt, f.failedPredecessor, f.omitReasonDeployment = true, failedDeployment, tc.omitReasonDeployment
				f.position, f.queueReads = 1, []dp.DeploymentQueueItemState{dp.Dispatched}
			})
			var diagnostics diag.Diagnostics
			require.Nil(t, resource.awaitQueuedDeployment(t.Context(), fixture.item(dp.Queued), &diagnostics))
			require.Len(t, diagnostics.Errors(), 1)
			require.Equal(t, "Deployment halted after a failure", diagnostics.Errors()[0].Summary())
			require.Contains(t, diagnostics.Errors()[0].Detail(), tc.want)
			if tc.unwanted != "" {
				require.NotContains(t, diagnostics.Errors()[0].Detail(), tc.unwanted)
			}
		})
	}
}

// A plan_only deployment that started while requests wait in the queue warns
// that its result may change; without pending changes there is no warning.
func TestPlanOnlyDeploymentWarnsWhenItsResultMayChange(t *testing.T) {
	for name, tc := range map[string]struct {
		waiting int
		want    []string
	}{
		"requests wait": {waiting: 2, want: []string{"The result may change: 0 Deployments running, 2 queued (result_may_change). " +
			"The plan_only deployment describes the environment as it is now, and those deployments change it first."}},
		"idle": {},
	} {
		t.Run(name, func(t *testing.T) {
			fixture, resource := newQueueTestResource(t)
			fixture.configure(func(f *deploymentQueueFixture) { f.running, f.waiting = false, tc.waiting })
			data := DeploymentResourceModel{
				ProjectId: types.StringValue("shop"), EnvId: types.StringValue("production"), Manifest: types.StringValue(`{"workloads":{}}`),
				Mode: types.StringValue("plan_only"), WaitFor: types.BoolValue(false), WhenBusy: types.StringNull(), HaltOnFailure: types.BoolNull(),
			}
			var diagnostics diag.Diagnostics
			require.NotNil(t, resource.doDeployment(t.Context(), &data, &diagnostics))
			require.False(t, diagnostics.HasError())
			var warnings []string
			for _, warning := range diagnostics.Warnings() {
				require.Equal(t, "Plan result may change", warning.Summary())
				warnings = append(warnings, warning.Detail())
			}
			require.Equal(t, tc.want, warnings)
		})
	}
	var diagnostics diag.Diagnostics
	addPendingChangesWarning(&diagnostics, &dp.PendingEnvironmentChanges{Code: dp.ResultMayChange, Running: 1, Queued: 0})
	require.Contains(t, diagnostics.Warnings()[0].Detail(), "The result may change: 1 Deployment running, 0 queued (result_may_change).")
}

// The Data Plane queues a deploy that omits when_busy, so the provider always
// sends it for deploys and never for plan_only.
func TestDeploymentSendsWhenBusyExplicitly(t *testing.T) {
	for name, tc := range map[string]struct {
		mode, whenBusy string
		want           *dp.DeploymentCreateBodyWhenBusy
	}{
		"unset":     {mode: "deploy", want: ref.Ref(dp.Queue)},
		"queue":     {mode: "deploy", whenBusy: whenBusyQueue, want: ref.Ref(dp.Queue)},
		"reject":    {mode: "deploy", whenBusy: whenBusyReject, want: ref.Ref(dp.Reject)},
		"plan_only": {mode: "plan_only", whenBusy: whenBusyReject},
	} {
		t.Run(name, func(t *testing.T) {
			fixture, resource := newQueueTestResource(t)
			fixture.configure(func(f *deploymentQueueFixture) {
				f.running, f.queueReads = false, []dp.DeploymentQueueItemState{dp.Dispatched}
			})
			data := DeploymentResourceModel{
				ProjectId: types.StringValue("shop"), EnvId: types.StringValue("production"), Manifest: types.StringValue(`{"workloads":{}}`),
				Mode: types.StringValue(tc.mode), WaitFor: types.BoolValue(false), WhenBusy: types.StringNull(),
			}
			if tc.whenBusy != "" {
				data.WhenBusy = types.StringValue(tc.whenBusy)
			}
			var diagnostics diag.Diagnostics
			resource.doDeployment(t.Context(), &data, &diagnostics)
			require.Equal(t, tc.want, fixture.lastCreate().WhenBusy)
		})
	}
}

// An outcome_unreconciled refusal names the fence the Data Plane reports and
// the operator action that lifts it. Without a fence, or without an answer,
// it still says what to do next.
func TestOutcomeUnreconciledNamesTheEnvironmentFence(t *testing.T) {
	fixture, resource := newQueueTestResource(t)
	window := time.Date(2026, 10, 7, 10, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	fixture.configure(func(f *deploymentQueueFixture) {
		f.fence = &dp.EnvironmentFence{Code: dp.OutcomeUnreconciled, DeploymentId: uuid.MustParse(fencingDeployment), Remedy: dp.AcknowledgeOutcome,
			Since: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC), RemedyAvailableAt: &window}
	})
	detail := resource.environmentFenceDetail(t.Context(), "shop", "production")
	require.Contains(t, detail, "fenced since 2026-10-07T09:00:00Z by failed plugin-dispatched deployment "+fencingDeployment)
	require.Contains(t, detail, "with the acknowledge-outcome action of deployment "+fencingDeployment+
		" (POST /internal/orgs/"+queueFixtureOrg+"/deployments/"+fencingDeployment+"/actions/acknowledge-outcome), accepted from 2026-10-07T08:00:00Z when")
	require.True(t, strings.HasSuffix(detail, "Acknowledging starts no deployment; apply again afterwards."), detail)

	fixture.configure(func(f *deploymentQueueFixture) { f.fence.RemedyAvailableAt = nil })
	detail = resource.environmentFenceDetail(t.Context(), "shop", "production")
	require.Contains(t, detail, "/actions/acknowledge-outcome). Acknowledging starts no deployment")

	fixture.configure(func(f *deploymentQueueFixture) { f.fence = nil })
	require.Equal(t, " The environment is no longer fenced: the outcome was acknowledged or its Runner reported. Apply again.",
		resource.environmentFenceDetail(t.Context(), "shop", "production"))

	fixture.configure(func(f *deploymentQueueFixture) { f.fenceUnreadable = true })
	require.Contains(t, resource.environmentFenceDetail(t.Context(), "shop", "production"),
		"A failed plugin-dispatched deployment may still be changing the environment.")
}
