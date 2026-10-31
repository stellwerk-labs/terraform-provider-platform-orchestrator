package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
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
	dpClient dp.ClientWithResponsesInterface
	orgId    string
}

type DeploymentResourceModel struct {
	DraftTests    types.Map      `tfsdk:"draft_test_snapshots"`
	ProjectId     types.String   `tfsdk:"project_id"`
	EnvId         types.String   `tfsdk:"env_id"`
	Manifest      types.String   `tfsdk:"manifest"`
	Mode          types.String   `tfsdk:"mode"`
	Id            types.String   `tfsdk:"id"`
	CreatedAt     types.String   `tfsdk:"created_at"`
	CompletedAt   types.String   `tfsdk:"completed_at"`
	Status        types.String   `tfsdk:"status"`
	StatusMessage types.String   `tfsdk:"status_message"`
	RunnerId      types.String   `tfsdk:"runner_id"`
	WaitFor       types.Bool     `tfsdk:"wait_for"`
	WhenBusy      types.String   `tfsdk:"when_busy"`
	HaltOnFailure types.Bool     `tfsdk:"halt_on_failure"`
	Outputs       types.String   `tfsdk:"outputs"`
	Timeouts      timeouts.Value `tfsdk:"timeouts"`
}

func (d *DeploymentResource) Metadata(ctx context.Context, request resource.MetadataRequest, response *resource.MetadataResponse) {
	response.TypeName = request.ProviderTypeName + "_deployment"
}

func (d *DeploymentResource) Schema(ctx context.Context, request resource.SchemaRequest, response *resource.SchemaResponse) {
	response.Schema = schema.Schema{
		// This description is used by the documentation generator and the language server.
		MarkdownDescription: "Deployment resource",

		Attributes: map[string]schema.Attribute{
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
				MarkdownDescription: "Whether to wait for the deployment to complete. Defaults to true. If false, the output will be empty.",
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
	d.orgId = providerData.OrgId
}

func (d *DeploymentResource) doDeployment(ctx context.Context, data *DeploymentResourceModel, diags *diag.Diagnostics) (outputsKey *age.X25519Identity) {
	selectedTests := validateDeploymentDraftTests(data.DraftTests, false, diags)
	if diags.HasError() {
		return nil
	}
	if data.Mode.IsNull() {
		data.Mode = types.StringValue(string(dp.DeploymentCreateBodyModeDeploy))
	}
	if data.WaitFor.IsNull() {
		data.WaitFor = types.BoolValue(true)
	}

	var manifest dp.DeploymentManifest
	if err := yaml.Unmarshal([]byte(data.Manifest.ValueString()), &manifest); err != nil {
		diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to parse manifest, got error: %s", err))
		return
	}

	outputsKey, _ = age.GenerateX25519Identity()
	body := dp.DeploymentCreateBody{
		ProjectId:                 data.ProjectId.ValueString(),
		EnvId:                     data.EnvId.ValueString(),
		Manifest:                  &manifest,
		Mode:                      dp.DeploymentCreateBodyMode(data.Mode.ValueString()),
		EncryptedOutputsRecipient: ref.Ref(outputsKey.Recipient().String()),
	}
	// Always explicit for deploys: the Data Plane queues a request without
	// when_busy, so reject has to be asked for. plan_only is never queued.
	if body.Mode == dp.DeploymentCreateBodyModeDeploy {
		body.WhenBusy = ref.Ref(dp.Queue)
		if data.WhenBusy.ValueString() == whenBusyReject {
			body.WhenBusy = ref.Ref(dp.Reject)
		} else if data.HaltOnFailure.ValueBool() {
			body.HaltOnFailure = ref.Ref(true)
		}
	}
	params := &dp.CreateDeploymentParams{IdempotencyKey: ref.Ref(uuid.NewString())}
	var r *dp.CreateDeploymentResponse
	var err error
	if len(selectedTests) > 0 {
		testParams := dp.CreateModuleTestDeploymentParams(*params)
		response, callErr := d.dpClient.CreateModuleTestDeploymentWithResponse(ctx, d.orgId, &testParams, dp.ModuleTestDeploymentCreateBody{Deployment: body, DraftTestSnapshots: selectedTests})
		r, err = (*dp.CreateDeploymentResponse)(response), callErr
	} else {
		r, err = d.dpClient.CreateDeploymentWithResponse(ctx, d.orgId, params, body)
	}
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
	case r.StatusCode() == http.StatusAccepted && r.JSON202 != nil:
		deployment = d.awaitQueuedDeployment(ctx, *r.JSON202, diags)
	case r.StatusCode() == http.StatusCreated && r.JSON201 != nil:
		deployment = r.JSON201
		addPendingChangesWarning(diags, deployment.PendingChanges)
	default:
		diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to create deployment, unexpected status code: %d, body: %s", r.StatusCode(), r.Body))
	}
	if deployment == nil {
		return nil
	}
	data.Id = types.StringValue(deployment.Id.String())
	data.CreatedAt = types.StringValue(deployment.CreatedAt.Format(time.RFC3339))
	data.CompletedAt = types.StringNull()
	data.Outputs = types.StringNull()
	data.Status = types.StringValue(deployment.Status)
	data.StatusMessage = types.StringValue(deployment.StatusMessage)
	data.RunnerId = types.StringValue(deployment.RunnerId)
	return outputsKey
}

func (d *DeploymentResource) waitForDeployment(ctx context.Context, data *DeploymentResourceModel, diags *diag.Diagnostics, outputsKey *age.X25519Identity) {
	deploymentUuid, err := uuid.Parse(data.Id.ValueString())
	if err != nil {
		diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to parse deployment ID, got error: %s", err))
		return
	}

	for {
		if r, err := d.dpClient.WaitForDeploymentCompleteWithResponse(ctx, d.orgId, deploymentUuid, &dp.WaitForDeploymentCompleteParams{}); err != nil {
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
		} else {
			data.Status = types.StringValue(r.JSON200.Status)
			data.StatusMessage = types.StringValue(r.JSON200.StatusMessage)
			data.CompletedAt = types.StringValue(r.JSON200.CompletedAt.Format(time.RFC3339))
			if data.Status.ValueString() == "succeeded" {
				if r, err := d.dpClient.GetDeploymentEncryptedOutputsWithResponse(ctx, d.orgId, deploymentUuid); err != nil {
					diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to read deployment outputs, got error: %s", err))
				} else if r.StatusCode() != http.StatusOK {
					diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to read deployment outputs, unexpected status code: %d, body: %s", r.StatusCode(), r.Body))
				} else {
					if decrypted, err := age.Decrypt(base64.NewDecoder(base64.StdEncoding, strings.NewReader(r.JSON200.Raw)), outputsKey); err != nil {
						diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to decrypt deployment outputs, got error: %s", err))
					} else if raw, err := io.ReadAll(decrypted); err != nil {
						diags.AddError(PO_API_ERR, fmt.Sprintf("Unable to read deployment outputs, got error: %s", err))
					} else {
						data.Outputs = types.StringValue(string(raw))
					}
				}
			} else {
				diags.AddError(PO_CLIENT_ERR, fmt.Sprintf("Deployment failed: %s", data.StatusMessage))
			}
			return
		}
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
	d.deploy(ctx, timeout, &data, &response.State, &response.Diagnostics)
}

// deploy creates the deployment, waits in the environment's deployment queue
// if needed and, with wait_for, for completion, all within timeout. A started
// deployment is always recorded in state, even when a later step fails.
func (d *DeploymentResource) deploy(ctx context.Context, timeout time.Duration, data *DeploymentResourceModel, state *tfsdk.State, diags *diag.Diagnostics) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	outputsKey := d.doDeployment(ctx, data, diags)
	if outputsKey == nil {
		return
	}
	diags.Append(state.Set(ctx, data)...)
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
		// Just refresh the status/status_message/completed_at fields.
		data.Status = types.StringValue(r.JSON200.Status)
		data.StatusMessage = types.StringValue(r.JSON200.StatusMessage)
		data.CompletedAt = types.StringNull()
		if r.JSON200.CompletedAt != nil {
			data.CompletedAt = types.StringValue(r.JSON200.CompletedAt.Format(time.RFC3339))
		}
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
	// when_busy, halt_on_failure and timeouts say how to deploy, not what:
	// changing only them, for example to wait longer in the queue, must not
	// redeploy.
	if data.ProjectId.Equal(prior.ProjectId) && data.EnvId.Equal(prior.EnvId) && data.Manifest.Equal(prior.Manifest) &&
		data.Mode.Equal(prior.Mode) && data.WaitFor.Equal(prior.WaitFor) && data.DraftTests.Equal(prior.DraftTests) {
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
	d.deploy(ctx, timeout, &data, &response.State, &response.Diagnostics)
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
