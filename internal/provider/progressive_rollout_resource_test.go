package provider

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/stretchr/testify/require"

	rollouts "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/progressive-rollouts"
)

const testRolloutSpec = `{"name":"payments","changes":[{"module_id":"payments","from":"1.0.0","target_version":"1.1.0","target_digest":"","target_status":"default"}],"selector":{"environment_ids":["11111111-1111-4111-8111-111111111111"]},"waves":[{"id":"canary","name":"Canary","environment_ids":["11111111-1111-4111-8111-111111111111"],"approval":"none","progression":"automatic","bake_time_seconds":0,"retry":{"max_attempts":1,"initial_delay_seconds":1,"maximum_delay_seconds":1,"maximum_duration_seconds":60},"max_concurrency":{"absolute":1}}],"default_promotion":"none"}`

func TestProgressiveRolloutControlAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		actual  rollouts.ProgressiveRolloutState
		desired string
		want    rolloutCommand
		ok      bool
	}{
		{name: "start", actual: rollouts.ProgressiveRolloutStateReady, desired: "running", want: rolloutCommandStart, ok: true},
		{name: "return to draft", actual: rollouts.ProgressiveRolloutStateReady, desired: "draft", want: rolloutCommandReturnToDraft, ok: true},
		{name: "pause", actual: rollouts.ProgressiveRolloutStateRunning, desired: "paused", want: rolloutCommandPause, ok: true},
		{name: "resume", actual: rollouts.ProgressiveRolloutStatePaused, desired: "running", want: rolloutCommandResume, ok: true},
		{name: "cancel and rollback running", actual: rollouts.ProgressiveRolloutStateRunning, desired: "rolled_back", want: rolloutCommandCancelAndRollback, ok: true},
		{name: "cancel and rollback paused", actual: rollouts.ProgressiveRolloutStatePaused, desired: "rolled_back", want: rolloutCommandCancelAndRollback, ok: true},
		{name: "rollback successful", actual: rollouts.ProgressiveRolloutStateSucceeded, desired: "rolled_back", want: rolloutCommandRollback, ok: true},
		{name: "rollback failed", actual: rollouts.ProgressiveRolloutStateFailed, desired: "rolled_back", want: rolloutCommandRollback, ok: true},
		{name: "cancel", actual: rollouts.ProgressiveRolloutStateRunning, desired: "cancelled", want: rolloutCommandCancel, ok: true},
		{name: "never restart terminal rollout", actual: rollouts.ProgressiveRolloutStateSucceeded, desired: "draft", ok: false},
		{name: "never cancel a rolled back rollout", actual: rollouts.ProgressiveRolloutStateRolledBack, desired: "cancelled", ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := progressiveRolloutControlAction(test.actual, test.desired)
			require.Equal(t, test.ok, ok)
			require.Equal(t, test.want, got)
		})
	}
}

func TestProgressiveRolloutDesiredStateCoversEveryServerState(t *testing.T) {
	t.Parallel()

	want := map[rollouts.ProgressiveRolloutState]string{
		rollouts.ProgressiveRolloutStateDraft:                      "draft",
		rollouts.ProgressiveRolloutStateReady:                      "ready",
		rollouts.ProgressiveRolloutStateRunning:                    "running",
		rollouts.ProgressiveRolloutStateAwaitingPinnedEnvironments: "running",
		rollouts.ProgressiveRolloutStateSucceeded:                  "running",
		rollouts.ProgressiveRolloutStateFailed:                     "running",
		rollouts.ProgressiveRolloutStatePaused:                     "paused",
		rollouts.ProgressiveRolloutStateCancelling:                 "cancelled",
		rollouts.ProgressiveRolloutStateCancelled:                  "cancelled",
		rollouts.ProgressiveRolloutStateRollingBack:                "rolled_back",
		rollouts.ProgressiveRolloutStateRolledBack:                 "rolled_back",
	}
	for state, desired := range want {
		got, known := progressiveRolloutDesiredState(state)
		require.True(t, known, state)
		require.Equal(t, desired, got, state)
	}
	// A superseded Rollout satisfies no request; import leaves the choice to
	// the configuration instead of guessing.
	_, known := progressiveRolloutDesiredState(rollouts.ProgressiveRolloutStateSuperseded)
	require.False(t, known)
}

func TestDecodeProgressiveRolloutSpecRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	_, err := decodeProgressiveRolloutSpec(jsontypes.NewNormalizedValue(testRolloutSpec))
	require.NoError(t, err)
	for _, value := range []string{`{"name":"payments","wavez":[]}`, `{"name":"x"} {"name":"y"}`, `[]`} {
		_, err := decodeProgressiveRolloutSpec(jsontypes.NewNormalizedValue(value))
		require.Error(t, err, value)
	}
}

func TestProgressiveRolloutSpecValueKeepsConfiguredTextUnlessTheServerDiffers(t *testing.T) {
	t.Parallel()

	var formatted bytes.Buffer
	require.NoError(t, json.Indent(&formatted, []byte(testRolloutSpec), "", "    "))
	configured := jsontypes.NewNormalizedValue(formatted.String())
	server, err := decodeProgressiveRolloutSpec(configured)
	require.NoError(t, err)

	kept, err := progressiveRolloutSpecValue(configured, server)
	require.NoError(t, err)
	require.Equal(t, configured.ValueString(), kept.ValueString(), "equal specs keep the configured text")

	server.Name = "payments-renamed"
	changed, err := progressiveRolloutSpecValue(configured, server)
	require.NoError(t, err)
	require.Contains(t, changed.ValueString(), `"payments-renamed"`, "a server-side change must surface as drift")

	imported, err := progressiveRolloutSpecValue(jsontypes.NewNormalizedNull(), server)
	require.NoError(t, err)
	require.Contains(t, imported.ValueString(), `"payments-renamed"`)
}

func TestProgressiveRolloutStatusJSONCarriesTheLicenceFields(t *testing.T) {
	t.Parallel()

	rolloutWithStatus := func(status string) rollouts.ProgressiveRollout {
		t.Helper()
		var rollout rollouts.ProgressiveRollout
		raw := `{"id":"22222222-2222-4222-8222-222222222222","org_id":"acme","state":"paused","resource_version":3,"spec":` + testRolloutSpec + `,"status":` + status + `}`
		require.NoError(t, json.Unmarshal([]byte(raw), &rollout))
		return rollout
	}
	statusJSON := func(rollout rollouts.ProgressiveRollout) string {
		t.Helper()
		var diagnostics diag.Diagnostics
		var state ProgressiveRolloutResourceModel
		applyProgressiveRolloutResourceState(&diagnostics, &state, rollout, nil)
		require.False(t, diagnostics.HasError(), diagnostics)
		return state.StatusJSON.ValueString()
	}
	const counts = `"total":1,"completed":0,"already_compliant":0,"not_applicable":0,"running":0,"blocked":0,"failed":0,"conflicts":0`

	warning := `{"state":"grace","grace_until":"2026-11-01T00:00:00Z"}`
	suspension := `{"state":"expired","suspended_at":"2026-10-01T12:00:00Z","resume_available":true,"resume_available_at":"2026-10-02T08:30:00Z"}`

	require.JSONEq(t, `{`+counts+`,"entitlement_warning":`+warning+`}`,
		statusJSON(rolloutWithStatus(`{`+counts+`,"entitlement_warning":`+warning+`}`)), "a grace warning reaches status_json")
	require.JSONEq(t, `{`+counts+`,"entitlement_suspension":`+suspension+`}`,
		statusJSON(rolloutWithStatus(`{`+counts+`,"entitlement_suspension":`+suspension+`}`)), "a licence suspension reaches status_json")
	require.JSONEq(t, `{`+counts+`}`, statusJSON(rolloutWithStatus(`{`+counts+`}`)), "a licensed Rollout adds neither field")
}
