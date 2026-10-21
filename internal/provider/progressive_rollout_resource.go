package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	rollouts "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/progressive-rollouts"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &ProgressiveRolloutResource{}
var _ resource.ResourceWithImportState = &ProgressiveRolloutResource{}
var _ resource.ResourceWithModifyPlan = &ProgressiveRolloutResource{}

func NewProgressiveRolloutResource() resource.Resource { return &ProgressiveRolloutResource{} }

type ProgressiveRolloutResource struct {
	client progressiveRolloutsClient
}

type ProgressiveRolloutResourceModel struct {
	ID                  types.String         `tfsdk:"id"`
	Spec                jsontypes.Normalized `tfsdk:"spec"`
	DesiredState        types.String         `tfsdk:"desired_state"`
	ControlReason       types.String         `tfsdk:"control_reason"`
	IdempotencyKey      types.String         `tfsdk:"idempotency_key"`
	State               types.String         `tfsdk:"state"`
	ResourceVersion     types.Int64          `tfsdk:"resource_version"`
	CurrentPlanRevision types.Int64          `tfsdk:"current_plan_revision"`
	PlanJSON            types.String         `tfsdk:"plan_json"`
	StatusJSON          types.String         `tfsdk:"status_json"`
}

const progressiveRolloutsRequirement = "Requires the Stellwerk Enterprise plugin " + progressiveRolloutsPluginDisplayName + " (`" + progressiveRolloutsPluginID + "`) to be installed, enabled and licensed for the organization. " +
	"Otherwise the provider reports `plugin_not_installed`, `plugin_not_enabled`, `plugin_not_entitled`, `plugin_incompatible` or `plugin_unavailable` with the administrator action. " +
	"After a licence expires, refresh, reads and the safety commands that Core still advertises (such as cancel and rollback) keep working, while new forward changes fail with `plugin_not_entitled`."

func (r *ProgressiveRolloutResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_progressive_rollout"
}

func (r *ProgressiveRolloutResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Enterprise: manages a persistent Progressive Rollout Draft, its immutable Plan preparation and high-level execution state. " + progressiveRolloutsRequirement +
			" Stellwerk permanently retains Rollout audit history, so destroy only forgets the state reference.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Persistent Rollout UUID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"spec": schema.StringAttribute{Required: true, CustomType: jsontypes.NormalizedType{}, MarkdownDescription: "JSON ProgressiveRolloutSpec. Unknown fields are rejected. Planning, selection, Wave generation and validation remain server-owned business logic. Changing the spec updates the Draft in place."},
			"desired_state": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("draft"),
				MarkdownDescription: "Desired high-level state: draft, ready, running, paused, cancelled or rolled_back. Preparing freezes a numbered immutable Plan; state transitions remain server validated.",
				Validators:          []validator.String{stringvalidator.OneOf("draft", "ready", "running", "paused", "cancelled", "rolled_back")},
			},
			"control_reason":        schema.StringAttribute{Optional: true, MarkdownDescription: "Human-readable reason attached to audited control transitions."},
			"idempotency_key":       schema.StringAttribute{Required: true, MarkdownDescription: "Stable, caller-owned key of the create request, 8 to 128 characters. Retrying with the same key returns the same Rollout. It is only used on creation; later changes are stored without effect.", Validators: []validator.String{stringvalidator.LengthBetween(8, 128)}},
			"state":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Actual server-owned Rollout state, including transitional and terminal states."},
			"resource_version":      schema.Int64Attribute{Computed: true, MarkdownDescription: "Optimistic-concurrency version."},
			"current_plan_revision": schema.Int64Attribute{Computed: true, MarkdownDescription: "Current immutable numbered Plan revision, null while the Draft has not been prepared."},
			"plan_json":             schema.StringAttribute{Computed: true, MarkdownDescription: "Current immutable Plan, including frozen membership and classification."},
			"status_json":           schema.StringAttribute{Computed: true, MarkdownDescription: "Current server-owned progress and terminal-result projection. Includes `entitlement_warning` (with `grace_until`) while the plugin licence is in its grace period and `entitlement_suspension` (licence state, `suspended_at`, `resume_available`) while the Rollout is paused because Core stopped allowing forward work."},
		},
	}
}

func (r *ProgressiveRolloutResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got %T", req.ProviderData))
		return
	}
	r.client = progressiveRolloutsClient{cp: data.CpClient, orgID: data.OrgId}
}

func decodeProgressiveRolloutSpec(value jsontypes.Normalized) (rollouts.ProgressiveRolloutSpec, error) {
	var spec rollouts.ProgressiveRolloutSpec
	decoder := json.NewDecoder(strings.NewReader(value.ValueString()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, fmt.Errorf("spec is not a valid ProgressiveRolloutSpec: %w", err)
	}
	if decoder.More() {
		return spec, fmt.Errorf("spec must contain exactly one JSON object")
	}
	return spec, nil
}

// progressiveRolloutSpecValue keeps the configured spec text when the server
// returns the same spec, so formatting never turns into drift. A real server
// change replaces it.
func progressiveRolloutSpecValue(current jsontypes.Normalized, server rollouts.ProgressiveRolloutSpec) (jsontypes.Normalized, error) {
	encoded, err := json.Marshal(server)
	if err != nil {
		return current, err
	}
	if !current.IsNull() && !current.IsUnknown() {
		if configured, err := decodeProgressiveRolloutSpec(current); err == nil {
			if canonical, err := json.Marshal(configured); err == nil && string(canonical) == string(encoded) {
				return current, nil
			}
		}
	}
	return jsontypes.NewNormalizedValue(string(encoded)), nil
}

func (r *ProgressiveRolloutResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ProgressiveRolloutResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec, err := decodeProgressiveRolloutSpec(plan.Spec)
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	if !r.client.require(ctx, &resp.Diagnostics, "createProgressiveRollout") {
		return
	}
	created, ok := invokeProgressiveRollouts[rollouts.ProgressiveRollout](ctx, r.client, progressiveRolloutsCall{
		action: "create Progressive Rollout", operationID: "createProgressiveRollout",
		body: spec, idempotencyKey: plan.IdempotencyKey.ValueString(), expectedStatus: http.StatusCreated,
	}, &resp.Diagnostics)
	if !ok {
		return
	}
	applyProgressiveRolloutResourceState(&resp.Diagnostics, &plan, created, nil)
	if resp.Diagnostics.HasError() {
		return
	}
	// Record the created Draft before any transition, so a failed transition
	// never leaves an untracked Rollout behind.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.DesiredState.ValueString() == "draft" {
		return
	}
	r.reconcileDesiredState(ctx, &plan, plan.DesiredState.ValueString(), &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ProgressiveRolloutResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ProgressiveRolloutResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.client.require(ctx, &resp.Diagnostics, "getProgressiveRollout") {
		return
	}
	detail, ok := r.client.get(ctx, state.ID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	applyProgressiveRolloutResourceState(&resp.Diagnostics, &state, detail.Rollout, detail.Plan)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.DesiredState.IsNull() || state.DesiredState.IsUnknown() {
		// Imports carry no configuration yet; adopt the request that matches the
		// actual state, so a matching configuration plans no transition.
		if desired, known := progressiveRolloutDesiredState(detail.Rollout.State); known {
			state.DesiredState = types.StringValue(desired)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ProgressiveRolloutResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ProgressiveRolloutResourceModel
	var state ProgressiveRolloutResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	current := state
	current.ControlReason = plan.ControlReason
	current.IdempotencyKey = plan.IdempotencyKey
	specChanged, desiredChanged, diagnostics := progressiveRolloutPluginChanges(ctx, plan, state)
	resp.Diagnostics.Append(diagnostics...)
	if resp.Diagnostics.HasError() {
		return
	}
	if specChanged {
		spec, err := decodeProgressiveRolloutSpec(plan.Spec)
		if err != nil {
			resp.Diagnostics.AddError(PO_INPUT_ERR, err.Error())
			return
		}
		rolloutID, err := uuid.Parse(state.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError(PO_INPUT_ERR, "id must be a Rollout UUID: "+err.Error())
			return
		}
		if !r.client.require(ctx, &resp.Diagnostics, "updateProgressiveRolloutDraft") {
			return
		}
		updated, ok := invokeProgressiveRollouts[rollouts.ProgressiveRollout](ctx, r.client, progressiveRolloutsCall{
			action: "update Progressive Rollout Draft", operationID: "updateProgressiveRolloutDraft",
			pathParameters: map[string]string{"rolloutId": rolloutID.String()},
			body:           rollouts.ProgressiveRolloutDraftUpdate{ExpectedResourceVersion: state.ResourceVersion.ValueInt64(), Spec: spec},
			idempotencyKey: progressiveRolloutCommandKey(rolloutID, "update", fmt.Sprint(state.ResourceVersion.ValueInt64()), plan.Spec.ValueString()),
			expectedStatus: http.StatusOK,
		}, &resp.Diagnostics)
		if !ok {
			return
		}
		current.Spec = plan.Spec
		applyProgressiveRolloutResourceState(&resp.Diagnostics, &current, updated, nil)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &current)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if desiredChanged && !r.reconcileDesiredState(ctx, &current, plan.DesiredState.ValueString(), &resp.Diagnostics) {
		// Keep every step that already happened. The previous desired_state
		// stays recorded, so the next apply retries from the actual state.
		resp.Diagnostics.Append(resp.State.Set(ctx, &current)...)
		return
	}
	current.DesiredState = plan.DesiredState
	resp.Diagnostics.Append(resp.State.Set(ctx, &current)...)
}

// progressiveRolloutPluginChanges reports what of plan reaches the plugin when
// it is applied over state: a changed spec updates the Draft, a changed
// desired_state transitions the Rollout. Nothing else is sent to the plugin.
func progressiveRolloutPluginChanges(ctx context.Context, plan, state ProgressiveRolloutResourceModel) (specChanged, desiredChanged bool, diagnostics diag.Diagnostics) {
	unchanged, diagnostics := plan.Spec.StringSemanticEquals(ctx, state.Spec)
	return !unchanged, plan.DesiredState.ValueString() != state.DesiredState.ValueString(), diagnostics
}

// ModifyPlan keeps the server-owned attributes known when an update reaches
// no plugin operation, because it changes only what Terraform alone stores:
// idempotency_key and control_reason. An import cannot read either back, so
// its first plan stores them instead of marking the whole Rollout unknown.
func (r *ProgressiveRolloutResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state ProgressiveRolloutResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.Spec.IsUnknown() || plan.DesiredState.IsUnknown() {
		return
	}
	specChanged, desiredChanged, diagnostics := progressiveRolloutPluginChanges(ctx, plan, state)
	resp.Diagnostics.Append(diagnostics...)
	if resp.Diagnostics.HasError() || specChanged || desiredChanged {
		return
	}
	// Exactly what Update stores: the prior state with the configured values.
	planned := state
	planned.Spec, planned.DesiredState, planned.ControlReason, planned.IdempotencyKey = plan.Spec, plan.DesiredState, plan.ControlReason, plan.IdempotencyKey
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &planned)...)
}

func (c progressiveRolloutsClient) get(ctx context.Context, rawID string, diagnostics *diag.Diagnostics) (rollouts.ProgressiveRolloutDetail, bool) {
	rolloutID, err := uuid.Parse(rawID)
	if err != nil {
		diagnostics.AddError(PO_INPUT_ERR, "id must be a Rollout UUID: "+err.Error())
		return rollouts.ProgressiveRolloutDetail{}, false
	}
	return invokeProgressiveRollouts[rollouts.ProgressiveRolloutDetail](ctx, c, progressiveRolloutsCall{
		action: "read Progressive Rollout", operationID: "getProgressiveRollout",
		pathParameters: map[string]string{"rolloutId": rolloutID.String()}, expectedStatus: http.StatusOK,
	}, diagnostics)
}

// reconcileDesiredState moves the Rollout towards desired with the fewest
// server-validated steps. The plugin remains the authority for every step.
func (r *ProgressiveRolloutResource) reconcileDesiredState(ctx context.Context, state *ProgressiveRolloutResourceModel, desired string, diagnostics *diag.Diagnostics) bool {
	actual := rollouts.ProgressiveRolloutState(state.State.ValueString())
	if current, known := progressiveRolloutDesiredState(actual); known && current == desired {
		return true
	}
	rolloutID, err := uuid.Parse(state.ID.ValueString())
	if err != nil {
		diagnostics.AddError(PO_INPUT_ERR, "id must be a Rollout UUID: "+err.Error())
		return false
	}
	if actual == rollouts.ProgressiveRolloutStateDraft && (desired == "ready" || desired == "running") {
		if !r.client.require(ctx, diagnostics, "previewProgressiveRollout", "prepareProgressiveRollout") {
			return false
		}
		spec, err := decodeProgressiveRolloutSpec(state.Spec)
		if err != nil {
			diagnostics.AddError(PO_INPUT_ERR, err.Error())
			return false
		}
		preview, ok := invokeProgressiveRollouts[rollouts.ProgressiveRolloutPlan](ctx, r.client, progressiveRolloutsCall{
			action: "preview Progressive Rollout", operationID: "previewProgressiveRollout", body: spec, expectedStatus: http.StatusOK,
		}, diagnostics)
		if !ok {
			return false
		}
		prepared, ok := invokeProgressiveRollouts[rollouts.ProgressiveRolloutDetail](ctx, r.client, progressiveRolloutsCall{
			action: "prepare Progressive Rollout", operationID: "prepareProgressiveRollout",
			pathParameters: map[string]string{"rolloutId": rolloutID.String()},
			body:           rollouts.ProgressiveRolloutPrepareBody{CandidateFingerprint: preview.Fingerprint, ExpectedResourceVersion: state.ResourceVersion.ValueInt64()},
			idempotencyKey: progressiveRolloutCommandKey(rolloutID, "prepare", preview.Fingerprint, state.ControlReason.ValueString()),
			expectedStatus: http.StatusOK,
		}, diagnostics)
		if !ok {
			return false
		}
		applyProgressiveRolloutResourceState(diagnostics, state, prepared.Rollout, prepared.Plan)
		if diagnostics.HasError() {
			return false
		}
		actual = prepared.Rollout.State
		if desired == "ready" {
			return true
		}
	}
	command, ok := progressiveRolloutControlAction(actual, desired)
	if !ok {
		diagnostics.AddError(PO_INPUT_ERR, fmt.Sprintf("unsupported Progressive Rollout state request %q -> %q", actual, desired))
		return false
	}
	if !r.client.require(ctx, diagnostics, "commandProgressiveRollout") {
		return false
	}
	reason := state.ControlReason.ValueString()
	body := rollouts.ProgressiveRolloutCommandBody{ExpectedResourceVersion: state.ResourceVersion.ValueInt64()}
	if reason != "" {
		body.Reason = &reason
	}
	result, ok := invokeProgressiveRollouts[rollouts.ProgressiveRolloutDetail](ctx, r.client, progressiveRolloutsCall{
		action: "control Progressive Rollout", operationID: "commandProgressiveRollout",
		pathParameters: map[string]string{"rolloutId": rolloutID.String(), "rolloutCommand": string(command)},
		body:           body,
		idempotencyKey: progressiveRolloutCommandKey(rolloutID, string(command), fmt.Sprint(state.ResourceVersion.ValueInt64()), reason),
		expectedStatus: http.StatusOK,
	}, diagnostics)
	if !ok {
		return false
	}
	applyProgressiveRolloutResourceState(diagnostics, state, result.Rollout, result.Plan)
	return !diagnostics.HasError()
}

// progressiveRolloutDesiredState names the desired_state that the actual state
// already satisfies. Terminal results of a started Rollout satisfy "running".
func progressiveRolloutDesiredState(actual rollouts.ProgressiveRolloutState) (string, bool) {
	switch actual {
	case rollouts.ProgressiveRolloutStateDraft:
		return "draft", true
	case rollouts.ProgressiveRolloutStateReady:
		return "ready", true
	case rollouts.ProgressiveRolloutStateRunning, rollouts.ProgressiveRolloutStateAwaitingPinnedEnvironments,
		rollouts.ProgressiveRolloutStateSucceeded, rollouts.ProgressiveRolloutStateFailed:
		return "running", true
	case rollouts.ProgressiveRolloutStatePaused:
		return "paused", true
	case rollouts.ProgressiveRolloutStateCancelling, rollouts.ProgressiveRolloutStateCancelled:
		return "cancelled", true
	case rollouts.ProgressiveRolloutStateRollingBack, rollouts.ProgressiveRolloutStateRolledBack:
		return "rolled_back", true
	default:
		return "", false
	}
}

// rolloutCommand is a rolloutCommand path value of the plugin operation
// commandProgressiveRollout. The contract test keeps these in its enum.
type rolloutCommand string

const (
	rolloutCommandStart             rolloutCommand = "start"
	rolloutCommandReturnToDraft     rolloutCommand = "return-to-draft"
	rolloutCommandPause             rolloutCommand = "pause"
	rolloutCommandResume            rolloutCommand = "resume"
	rolloutCommandCancel            rolloutCommand = "cancel"
	rolloutCommandCancelAndRollback rolloutCommand = "cancel-and-rollback"
	rolloutCommandRollback          rolloutCommand = "rollback"
)

func progressiveRolloutControlAction(actual rollouts.ProgressiveRolloutState, desired string) (rolloutCommand, bool) {
	switch {
	case actual == rollouts.ProgressiveRolloutStateReady && desired == "running":
		return rolloutCommandStart, true
	case actual == rollouts.ProgressiveRolloutStateReady && desired == "draft":
		return rolloutCommandReturnToDraft, true
	case actual == rollouts.ProgressiveRolloutStateRunning && desired == "paused":
		return rolloutCommandPause, true
	case actual == rollouts.ProgressiveRolloutStatePaused && desired == "running":
		return rolloutCommandResume, true
	case (actual == rollouts.ProgressiveRolloutStateRunning || actual == rollouts.ProgressiveRolloutStatePaused) && desired == "rolled_back":
		return rolloutCommandCancelAndRollback, true
	case (actual == rollouts.ProgressiveRolloutStateSucceeded || actual == rollouts.ProgressiveRolloutStateFailed || actual == rollouts.ProgressiveRolloutStateCancelled) && desired == "rolled_back":
		return rolloutCommandRollback, true
	case desired == "cancelled" && actual != rollouts.ProgressiveRolloutStateCancelled && actual != rollouts.ProgressiveRolloutStateRolledBack:
		return rolloutCommandCancel, true
	default:
		return "", false
	}
}

func progressiveRolloutCommandKey(id uuid.UUID, parts ...string) string {
	material := "terraform"
	for _, part := range parts {
		material += "\x00" + part
	}
	return uuid.NewSHA1(id, []byte(material)).String()
}

func applyProgressiveRolloutResourceState(diagnostics *diag.Diagnostics, state *ProgressiveRolloutResourceModel, rollout rollouts.ProgressiveRollout, plan *rollouts.ProgressiveRolloutPlan) {
	state.ID = types.StringValue(rollout.Id.String())
	state.State = types.StringValue(string(rollout.State))
	state.ResourceVersion = types.Int64Value(rollout.ResourceVersion)
	if rollout.CurrentPlanRevision == nil {
		state.CurrentPlanRevision = types.Int64Null()
	} else {
		state.CurrentPlanRevision = types.Int64Value(int64(*rollout.CurrentPlanRevision))
	}
	spec, err := progressiveRolloutSpecValue(state.Spec, rollout.Spec)
	if err != nil {
		diagnostics.AddError(PO_PROVIDER_ERR, "Unable to encode Progressive Rollout spec: "+err.Error())
		return
	}
	state.Spec = spec
	if !setJSONString(diagnostics, &state.StatusJSON, rollout.Status) {
		return
	}
	if plan == nil {
		state.PlanJSON = types.StringNull()
		return
	}
	setJSONString(diagnostics, &state.PlanJSON, plan)
}

func (r *ProgressiveRolloutResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("Progressive Rollout retained", "Stellwerk permanently retains Rollout Plans, execution state and audit history. Terraform removed only its state reference; no cancel, rollback or archive command was inferred.")
	resp.State.RemoveResource(ctx)
}

func (r *ProgressiveRolloutResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := uuid.Parse(req.ID); err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, "Import ID must be a Rollout UUID")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
