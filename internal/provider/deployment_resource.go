package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"gopkg.in/yaml.v3"

	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/pipelines"
	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/ref"
)

var _ resource.Resource = &DeploymentResource{}
var _ resource.ResourceWithConfigure = &DeploymentResource{}
var _ resource.ResourceWithValidateConfig = &DeploymentResource{}

func NewDeploymentResource() resource.Resource {
	return &DeploymentResource{}
}

type DeploymentResource struct {
	dpClient              dp.ClientWithResponsesInterface
	pipelineClient        pipelines.ClientWithResponsesInterface
	orgId                 string
	scopeFingerprint      string
	credentialFingerprint string
}

type DeploymentResourceModel struct {
	PipelineVersionID types.String   `tfsdk:"pipeline_version_id"`
	DraftTests        types.Map      `tfsdk:"draft_test_snapshots"`
	ProjectId         types.String   `tfsdk:"project_id"`
	EnvId             types.String   `tfsdk:"env_id"`
	Manifest          types.String   `tfsdk:"manifest"`
	Mode              types.String   `tfsdk:"mode"`
	Id                types.String   `tfsdk:"id"`
	CreatedAt         types.String   `tfsdk:"created_at"`
	CompletedAt       types.String   `tfsdk:"completed_at"`
	Status            types.String   `tfsdk:"status"`
	StatusMessage     types.String   `tfsdk:"status_message"`
	RunnerId          types.String   `tfsdk:"runner_id"`
	WaitFor           types.Bool     `tfsdk:"wait_for"`
	WhenBusy          types.String   `tfsdk:"when_busy"`
	HaltOnFailure     types.Bool     `tfsdk:"halt_on_failure"`
	Outputs           types.String   `tfsdk:"outputs"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func (d *DeploymentResource) Metadata(ctx context.Context, request resource.MetadataRequest, response *resource.MetadataResponse) {
	response.TypeName = request.ProviderTypeName + "_deployment"
}

func (d *DeploymentResource) Schema(ctx context.Context, request resource.SchemaRequest, response *resource.SchemaResponse) {
	response.Schema = schema.Schema{
		// This description is used by the documentation generator and the language server.
		MarkdownDescription: "Deployment resource.\n\n" +
			"Once Terraform has retained an accepted deployment and its private output identity, refresh can retrieve that deployment's outputs without deploying again. Protect the Terraform state backend: private resource metadata contains encryption material and is not encrypted by the provider.\n\n" +
			"If the deployment has conclusively succeeded but outputs cannot be retrieved, apply reports a warning and retains the successful deployment without taint. Outputs remain empty until an authorized refresh can retrieve and decrypt them.\n\n" +
			"A create error can leave the resource tainted. Refresh does not remove taint, so verify the accepted deployment before another apply, which may create a replacement. Termination before Terraform receives and saves the response can still lose custody, including when applying a saved plan. Historical output identities that were not retained cannot be recovered.",

		Attributes: map[string]schema.Attribute{
			"pipeline_version_id": schema.StringAttribute{Optional: true, MarkdownDescription: "Exact published pipeline Version UUID. Resolved through Core to its immutable numeric version; the server validates policy, Runner compatibility and executable digest. Omission uses the server default and preserves existing deployments on upgrade. Changing the selection starts a new deployment; it never edits a recipe or approves its gates."},
			"draft_test_snapshots": schema.MapAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Exact captured Module tests, mapping stable Module UUIDs to immutable snapshot UUIDs scoped to this Environment. These use a separate server operation and never fall back to published Defaults on an older server. Capture with platform-orchestrator_module_draft_test first. Changing a selection creates a new deployment; it does not edit the snapshot or publish a Version.",
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "The project ID to deploy.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[a-z](?:-?[a-z0-9]+)+$`),
						"must start with a lowercase letter, can contain lowercase letters, numbers, and hyphens and can not be empty.",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"env_id": schema.StringAttribute{
				MarkdownDescription: "The environment ID to deploy.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						regexp.MustCompile(`^[a-z](?:-?[a-z0-9]+)+$`),
						"must start with a lowercase letter, can contain lowercase letters, numbers, and hyphens and can not be empty.",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"manifest": schema.StringAttribute{
				MarkdownDescription: "The YAML/JSON encoded manifest to deploy.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"mode": schema.StringAttribute{
				MarkdownDescription: "The mode of the deployment. 'deploy' (the default) or 'plan_only'. " +
					"A `plan_only` deployment is never queued and does not run beside another deployment of the environment; " +
					"while deployments wait in the environment's deployment queue, it plans the environment as it is now and the apply warns that the result may change.",
				Computed: true,
				Optional: true,
				Default:  stringdefault.StaticString(string(dp.DeploymentCreateBodyModeDeploy)),
				Validators: []validator.String{
					stringvalidator.OneOf(string(dp.DeploymentCreateBodyModeDeploy), string(dp.DeploymentCreateBodyModePlanOnly)),
				},
			},
			"id": schema.StringAttribute{
				MarkdownDescription: "The ID of the Deployment.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "The date and time when the deployment was created.",
				Computed:            true,
			},
			"completed_at": schema.StringAttribute{
				MarkdownDescription: "The date and time when the deployment was completed.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The status of the deployment (succeeded, failed).",
				Computed:            true,
			},
			"status_message": schema.StringAttribute{
				MarkdownDescription: "An optional message associated with the status.",
				Computed:            true,
			},
			"runner_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the runner used in this deployment.",
				Computed:            true,
			},
			"wait_for": schema.BoolAttribute{
				MarkdownDescription: "Whether to wait for the deployment to complete. Defaults to true. If false, outputs are initially empty and a later refresh retrieves them after successful completion.",
				Computed:            true,
				Optional:            true,
				Default:             booldefault.StaticBool(true),
			},
			"outputs": schema.StringAttribute{
				MarkdownDescription: "The JSON encoded outputs of the deployment.",
				Computed:            true,
				Sensitive:           true,
			},
			// Deliberately without a schema default: null means queue, so
			// existing state plans no change and no redeploy after an upgrade.
			"when_busy": schema.StringAttribute{
				MarkdownDescription: "What the deployment does when its environment is busy with another deployment or already has queued deployments. " +
					"`queue` (the default) waits in the environment's deployment queue and starts in order once the environment is free; " +
					"the create or update timeout covers that wait, and a request still queued when it elapses is cancelled. " +
					"`reject` fails with `environment_busy` instead. " +
					"Leaving it unset means `queue`, which is also the Data Plane's own default; the provider sends the value explicitly either way. " +
					"A provider authorized by an organization token cannot wait in the queue and gets `environment_busy` from a busy environment. " +
					"`plan_only` deployments are never queued. Changing only `when_busy` updates state without redeploying.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf(whenBusyQueue, whenBusyReject),
				},
			},
			// Without a schema default for the same reason as when_busy: null
			// means false.
			"halt_on_failure": schema.BoolAttribute{
				MarkdownDescription: "Whether a deployment that waits in the environment's deployment queue must not start when the deployment it waited for failed. " +
					"The queued request is then rejected with `previous_deployment_failed`, the apply fails and names the failed deployment, " +
					"and the queue continues with the next request. Unset means `false`. " +
					"A deployment that starts at once never halts, and `plan_only` deployments are never queued. " +
					"Not valid with `when_busy = \"reject\"`. Changing only `halt_on_failure` updates state without redeploying.",
				Optional: true,
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{
				Create:            true,
				Update:            true,
				Delete:            true,
				CreateDescription: "A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration), for example `60m`. How long to create the deployment, including its wait in the environment's deployment queue and, with `wait_for`, for its completion. Defaults to 20m.",
				UpdateDescription: "A string that can be [parsed as a duration](https://pkg.go.dev/time#ParseDuration), for example `60m`. How long to redeploy, including the wait in the environment's deployment queue and, with `wait_for`, for completion. Defaults to 20m.",
			}),
		},
	}
}

// ValidateConfig refuses halt_on_failure with when_busy = "reject": only a
// deployment that may wait in the queue can halt, and the Data Plane answers
// the combination with 400.
func (d *DeploymentResource) ValidateConfig(ctx context.Context, request resource.ValidateConfigRequest, response *resource.ValidateConfigResponse) {
	var tests types.Map
	response.Diagnostics.Append(request.Config.GetAttribute(ctx, path.Root("draft_test_snapshots"), &tests)...)
	validateDeploymentDraftTests(tests, true, &response.Diagnostics)
	var selectedPipeline types.String
	response.Diagnostics.Append(request.Config.GetAttribute(ctx, path.Root("pipeline_version_id"), &selectedPipeline)...)
	if !selectedPipeline.IsNull() && !selectedPipeline.IsUnknown() {
		id, err := uuid.Parse(selectedPipeline.ValueString())
		if err != nil || id == uuid.Nil {
			response.Diagnostics.AddAttributeError(path.Root("pipeline_version_id"), PO_INPUT_ERR, "Must identify an exact published pipeline version UUID.")
		}
	}
	var whenBusy types.String
	var halt types.Bool
	response.Diagnostics.Append(request.Config.GetAttribute(ctx, path.Root("when_busy"), &whenBusy)...)
	response.Diagnostics.Append(request.Config.GetAttribute(ctx, path.Root("halt_on_failure"), &halt)...)
	if halt.ValueBool() && whenBusy.ValueString() == whenBusyReject {
		response.Diagnostics.AddAttributeError(path.Root("halt_on_failure"), "Invalid halt_on_failure",
			`halt_on_failure = true only applies to a deployment that may wait in the environment's deployment queue; it is not valid with when_busy = "reject".`)
	}
}

func (d *DeploymentResource) Configure(ctx context.Context, request resource.ConfigureRequest, response *resource.ConfigureResponse) {
	if request.ProviderData == nil {
		return
	}

	providerData, ok := request.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		response.Diagnostics.AddError(
			PO_PROVIDER_ERR,
			fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got: %T. Please report this issue to the provider developers.", request.ProviderData),
		)
		return
	}

	d.dpClient = providerData.DpClient
	d.pipelineClient = providerData.PipelineClient
	d.orgId = providerData.OrgId
	d.scopeFingerprint = providerData.deploymentScope
	d.credentialFingerprint = providerData.deploymentCredential
}

func (d *DeploymentResource) deploymentBody(data DeploymentResourceModel) (dp.DeploymentCreateBody, error) {
	var body dp.DeploymentCreateBody
	if data.ProjectId.IsUnknown() || data.EnvId.IsUnknown() || data.Manifest.IsUnknown() || data.Mode.IsUnknown() || data.PipelineVersionID.IsUnknown() {
		return body, fmt.Errorf("deployment inputs must be known before admission")
	}
	var manifest dp.DeploymentManifest
	if err := yaml.Unmarshal([]byte(data.Manifest.ValueString()), &manifest); err != nil {
		return body, fmt.Errorf("unable to parse deployment manifest")
	}
	mode := dp.DeploymentCreateBodyMode(data.Mode.ValueString())
	if data.Mode.IsNull() {
		mode = dp.DeploymentCreateBodyModeDeploy
	}
	body = dp.DeploymentCreateBody{ProjectId: data.ProjectId.ValueString(), EnvId: data.EnvId.ValueString(), Manifest: &manifest, Mode: mode}
	if mode == dp.DeploymentCreateBodyModeDeploy {
		body.WhenBusy = ref.Ref(dp.Queue)
		if data.WhenBusy.ValueString() == whenBusyReject {
			body.WhenBusy = ref.Ref(dp.Reject)
		} else if data.HaltOnFailure.ValueBool() {
			body.HaltOnFailure = ref.Ref(true)
		}
	}
	return body, nil
}

func (d *DeploymentResource) doDeployment(ctx context.Context, data *DeploymentResourceModel, operation *deploymentOperation, diags *diag.Diagnostics) (outputsKey *age.X25519Identity, refused bool) {
	outputsKey, err := operation.identity()
	if err != nil {
		diags.AddError(PO_PROVIDER_ERR, "Invalid retained deployment output custody.")
		return nil, false
	}
	body := operation.Request.Deployment
	r, err := d.submitPreparedDeployment(ctx, operation)
	var deployment *dp.Deployment
	switch {
	case err != nil:
		diags.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to create deployment, got error: %s", err))
	case r.StatusCode() == http.StatusConflict:
		fenced := ""
		if r.JSON409 != nil && r.JSON409.Error == string(dp.OutcomeUnreconciled) {
			fenced = d.environmentFenceDetail(ctx, body.ProjectId, body.EnvId)
		}
		addDeploymentConflictError(diags, body.WhenBusy, r.JSON409, r.Body, fenced)
		refused = deploymentAdmissionRefused(r.StatusCode(), r.JSON409)
	case r.StatusCode() == http.StatusAccepted && r.JSON202 != nil:
		deployment, refused = d.awaitPreparedQueue(ctx, body, *r.JSON202, diags)
	case r.StatusCode() == http.StatusCreated && r.JSON201 != nil:
		deployment = r.JSON201
		addPendingChangesWarning(diags, deployment.PendingChanges)
	default:
		diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to create deployment, unexpected status code: %d, body: %s", r.StatusCode(), r.Body))
		refused = deploymentAdmissionRefused(r.StatusCode(), nil)
	}
	if deployment == nil {
		return nil, refused
	}
	if err := operation.bindAdmission(deployment); err != nil {
		diags.AddError(PO_API_ERR, err.Error())
		return nil, false
	}
	data.Id = types.StringValue(deployment.Id.String())
	data.CreatedAt = types.StringValue(deployment.CreatedAt.Format(time.RFC3339))
	data.CompletedAt = types.StringNull()
	data.Outputs = types.StringNull()
	data.Status = types.StringValue(deployment.Status)
	data.StatusMessage = types.StringValue(deployment.StatusMessage)
	data.RunnerId = types.StringValue(deployment.RunnerId)
	return outputsKey, false
}

func (d *DeploymentResource) waitForDeployment(ctx context.Context, data *DeploymentResourceModel, diags *diag.Diagnostics, outputsKey *age.X25519Identity) {
	deploymentUuid, err := uuid.Parse(data.Id.ValueString())
	if err != nil {
		diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to parse deployment ID, got error: %s", err))
		return
	}

	for {
		r, err := d.dpClient.WaitForDeploymentCompleteWithResponse(ctx, d.orgId, deploymentUuid, &dp.WaitForDeploymentCompleteParams{})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			}
			diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to wait for deployment to complete, got error: %s", err))
			return
		} else if r.StatusCode() == http.StatusRequestTimeout {
			if err := ctx.Err(); err != nil {
				diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to wait for deployment to complete, got error: %s", err))
				return
			}
			continue
		} else if r.StatusCode() != http.StatusOK {
			diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to wait for deployment to complete, unexpected status code: %d, body: %s", r.StatusCode(), r.Body))
			return
		}
		if !deploymentMatchesScope(r.JSON200, d.orgId, data.ProjectId.ValueString(), data.EnvId.ValueString()) || r.JSON200.Id != deploymentUuid || r.JSON200.CompletedAt == nil {
			diags.AddError(PO_API_ERR, "Deployment completion identity, scope or completion timestamp did not match the accepted deployment.")
			return
		}
		data.Status = types.StringValue(r.JSON200.Status)
		data.StatusMessage = types.StringValue(r.JSON200.StatusMessage)
		data.CompletedAt = types.StringValue(r.JSON200.CompletedAt.Format(time.RFC3339))
		if data.Status.ValueString() != "succeeded" {
			diags.AddError(PO_CLIENT_ERR, fmt.Sprintf("Deployment failed: %s", data.StatusMessage))
			return
		}
		var outputsDiagnostics diag.Diagnostics
		d.readDeploymentOutputs(ctx, deploymentUuid, outputsKey, data, &outputsDiagnostics)
		if outputsDiagnostics.HasError() {
			diags.AddWarning("Deployment succeeded; outputs unavailable", "The deployment completed successfully, but its outputs could not be retrieved or decrypted. The accepted deployment and its private output identity were retained. Outputs remain empty until an authorized refresh can retrieve and decrypt them; no new deployment is needed.")
		}
		return
	}
}

func (d *DeploymentResource) Create(ctx context.Context, request resource.CreateRequest, response *resource.CreateResponse) {
	var data DeploymentResourceModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &data)...)
	if response.Diagnostics.HasError() {
		return
	}
	timeout, diags := data.Timeouts.Create(ctx, DefaultAsyncTimeout)
	response.Diagnostics.Append(diags...)
	if response.Diagnostics.HasError() {
		return
	}
	d.deploy(ctx, timeout, &data, nil, response.Private, deploymentCustody{Version: 1}, &response.State, &response.Diagnostics)
}

// deploy creates the deployment, waits in the environment's deployment queue
// if needed and, with wait_for, for completion, all within timeout. A started
// deployment is always recorded in state, even when a later step fails.
func (d *DeploymentResource) deploy(ctx context.Context, timeout time.Duration, data, prior *DeploymentResourceModel, private deploymentPrivateData, custody deploymentCustody, state *tfsdk.State, diags *diag.Diagnostics) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if data.WaitFor.IsUnknown() {
		diags.AddError(PO_INPUT_ERR, "wait_for must be known before deployment")
		return
	}
	if data.Mode.IsNull() {
		data.Mode = types.StringValue(string(dp.DeploymentCreateBodyModeDeploy))
	}
	if data.WaitFor.IsNull() {
		data.WaitFor = types.BoolValue(true)
	}
	retrying := custody.Pending != nil
	operation, err := d.prepareDeployment(ctx, data, custody)
	if err != nil {
		diags.AddError(PO_INPUT_ERR, err.Error())
		return
	}
	custody.Pending = operation
	diags.Append(storeDeploymentCustody(ctx, private, custody)...)
	if diags.HasError() {
		return
	}
	outputsKey, refused := d.doDeployment(ctx, data, operation, diags)
	if outputsKey == nil {
		if refused && !retrying {
			custody.Pending = nil
		}
		diags.Append(storeDeploymentCustody(ctx, private, custody)...)
		if prior != nil {
			diags.Append(state.Set(ctx, prior)...)
		}
		return
	}
	custody.Current = operation
	custody.Pending = nil
	diags.Append(state.Set(ctx, data)...)
	diags.Append(storeDeploymentCustody(ctx, private, custody)...)
	if diags.HasError() || !data.WaitFor.ValueBool() {
		return
	}
	d.waitForDeployment(ctx, data, diags, outputsKey)
	diags.Append(state.Set(ctx, data)...)
}

func (d *DeploymentResource) Read(ctx context.Context, request resource.ReadRequest, response *resource.ReadResponse) {
	var data DeploymentResourceModel
	response.Diagnostics.Append(request.State.Get(ctx, &data)...)
	if response.Diagnostics.HasError() {
		return
	}

	custody, err := readDeploymentCustody(ctx, request.Private)
	if err == nil {
		err = d.validateDeploymentCustody(custody, data)
	}
	if err != nil {
		response.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	deploymentUuid, err := uuid.Parse(data.Id.ValueString())
	if err != nil {
		response.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to parse deployment ID, got error: %s", err))
		return
	}

	if r, err := d.dpClient.GetDeploymentWithResponse(ctx, d.orgId, deploymentUuid); err != nil {
		response.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Unable to read deployment, got error: %s", err))
		return
	} else if r.StatusCode() == http.StatusNotFound {
		response.Diagnostics.AddWarning(PO_RESOURCE_NOT_FOUND_ERR, fmt.Sprintf("Deployment with ID %s not found, assuming it has been deleted.", data.Id.ValueString()))
		response.State.RemoveResource(ctx)
		return
	} else if r.StatusCode() != http.StatusOK {
		response.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to read deployment, unexpected status code: %d, body: %s", r.StatusCode(), r.Body))
		return
	} else {
		if !deploymentMatchesScope(r.JSON200, d.orgId, data.ProjectId.ValueString(), data.EnvId.ValueString()) || r.JSON200.Id != deploymentUuid {
			response.Diagnostics.AddError(PO_API_ERR, "Deployment read identity or scope did not match retained state.")
			return
		}
		// Refresh the same accepted object; this path never admits a deployment.
		data.Status = types.StringValue(r.JSON200.Status)
		data.StatusMessage = types.StringValue(r.JSON200.StatusMessage)
		data.CompletedAt = types.StringNull()
		if r.JSON200.CompletedAt != nil {
			data.CompletedAt = types.StringValue(r.JSON200.CompletedAt.Format(time.RFC3339))
		}
	}
	if data.Status.ValueString() == "succeeded" {
		d.recoverDeploymentOutputs(ctx, custody.Current, &data, &response.Diagnostics)
	}
	response.Diagnostics.Append(response.State.Set(ctx, &data)...)
}

func (d *DeploymentResource) Update(ctx context.Context, request resource.UpdateRequest, response *resource.UpdateResponse) {
	var data, prior DeploymentResourceModel
	response.Diagnostics.Append(request.Plan.Get(ctx, &data)...)
	response.Diagnostics.Append(request.State.Get(ctx, &prior)...)
	if response.Diagnostics.HasError() {
		return
	}
	custody, err := readDeploymentCustody(ctx, request.Private)
	if err == nil {
		err = d.validateDeploymentCustody(custody, prior)
	}
	if err != nil {
		response.Diagnostics.AddError(PO_PROVIDER_ERR, err.Error())
		return
	}
	// when_busy, halt_on_failure and timeouts say how to deploy, not what:
	// changing only them, for example to wait longer in the queue, must not
	// redeploy.
	if custody.Pending == nil && data.ProjectId.Equal(prior.ProjectId) && data.EnvId.Equal(prior.EnvId) && data.Manifest.Equal(prior.Manifest) &&
		data.Mode.Equal(prior.Mode) && data.WaitFor.Equal(prior.WaitFor) && data.DraftTests.Equal(prior.DraftTests) && data.PipelineVersionID.Equal(prior.PipelineVersionID) {
		data.Id, data.CreatedAt, data.CompletedAt, data.Outputs = prior.Id, prior.CreatedAt, prior.CompletedAt, prior.Outputs
		data.Status, data.StatusMessage, data.RunnerId = prior.Status, prior.StatusMessage, prior.RunnerId
		response.Diagnostics.Append(response.State.Set(ctx, &data)...)
		return
	}
	timeout, diags := data.Timeouts.Update(ctx, DefaultAsyncTimeout)
	response.Diagnostics.Append(diags...)
	if response.Diagnostics.HasError() {
		return
	}
	d.deploy(ctx, timeout, &data, &prior, response.Private, custody, &response.State, &response.Diagnostics)
}

func (d *DeploymentResource) Delete(ctx context.Context, request resource.DeleteRequest, response *resource.DeleteResponse) {
}

func validateDeploymentDraftTests(value types.Map, allowUnknown bool, diagnostics *diag.Diagnostics) map[string]uuid.UUID {
	if value.IsNull() {
		return nil
	}
	if value.IsUnknown() {
		if !allowUnknown {
			diagnostics.AddError(PO_INPUT_ERR, "Draft test selection is still unknown at deployment")
		}
		return nil
	}
	const maxSelections = 1000
	if len(value.Elements()) > maxSelections {
		diagnostics.AddError(PO_INPUT_ERR, "Select at most 1000 immutable Draft test snapshots")
		return nil
	}
	selected := make(map[string]uuid.UUID, len(value.Elements()))
	for key, raw := range value.Elements() {
		module, err := uuid.Parse(key)
		if err != nil || module == uuid.Nil || module.String() != key {
			diagnostics.AddAttributeError(path.Root("draft_test_snapshots"), PO_INPUT_ERR, "Draft test keys must be canonical nonzero Module UUIDs")
			continue
		}
		snapshotValue, ok := raw.(types.String)
		if !ok {
			diagnostics.AddAttributeError(path.Root("draft_test_snapshots"), PO_INPUT_ERR, "Draft test values must be snapshot UUID strings")
			continue
		}
		if snapshotValue.IsUnknown() && allowUnknown {
			continue
		}
		snapshot, err := uuid.Parse(snapshotValue.ValueString())
		if err != nil || snapshot == uuid.Nil || snapshot.String() != snapshotValue.ValueString() {
			diagnostics.AddAttributeError(path.Root("draft_test_snapshots"), PO_INPUT_ERR, "Draft test values must be canonical nonzero captured snapshot UUIDs")
			continue
		}
		selected[key] = snapshot
	}
	return selected
}
