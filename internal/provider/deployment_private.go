package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/justinrixx/retryhttp"
	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
)

const deploymentPrivateKey = "deployment_operation_v1"
const deploymentPrivateLimit = 8 * 1024 * 1024

type deploymentPrivateData interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
	SetKey(context.Context, string, []byte) diag.Diagnostics
}
type deploymentCustody struct {
	Version int                  `json:"version"`
	Current *deploymentOperation `json:"current,omitempty"`
	Pending *deploymentOperation `json:"pending,omitempty"`
}
type deploymentOperation struct {
	Intent          string                            `json:"intent"`
	Scope           string                            `json:"scope"`
	Credential      string                            `json:"credential"`
	RequestHash     string                            `json:"request_hash"`
	RequestKey      string                            `json:"request_key"`
	OutputsIdentity string                            `json:"outputs_identity"`
	AcceptedID      string                            `json:"accepted_id,omitempty"`
	OrgID           string                            `json:"org_id"`
	Request         dp.ModuleTestDeploymentCreateBody `json:"request"`
}

func deploymentDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func deploymentAuthority(apiURL, org string, headers http.Header) (string, string) {
	return deploymentDigest([]string{apiURL, org}), deploymentDigest(headers)
}
func deploymentPrivateError() error {
	return fmt.Errorf("retained deployment custody is invalid or does not match this operation; no new deployment was requested")
}

func readDeploymentCustody(ctx context.Context, private deploymentPrivateData) (deploymentCustody, error) {
	value := deploymentCustody{Version: 1}
	if private == nil {
		return value, deploymentPrivateError()
	}
	raw, diags := private.GetKey(ctx, deploymentPrivateKey)
	if diags.HasError() {
		return value, deploymentPrivateError()
	}
	return decodeDeploymentCustody(raw)
}
func decodeDeploymentCustody(raw []byte) (deploymentCustody, error) {
	if len(raw) == 0 {
		return deploymentCustody{Version: 1}, nil
	}
	var value deploymentCustody
	if len(raw) > deploymentPrivateLimit {
		return value, deploymentPrivateError()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return value, deploymentPrivateError()
	}
	if err := value.validate(); err != nil {
		return value, err
	}
	return value, nil
}
func (value deploymentCustody) validate() error {
	if value.Version != 1 {
		return deploymentPrivateError()
	}
	for _, operation := range []*deploymentOperation{value.Current, value.Pending} {
		if operation != nil {
			if err := operation.validate(); err != nil {
				return err
			}
		}
	}
	if value.Current != nil && value.Current.AcceptedID == "" {
		return deploymentPrivateError()
	}
	return nil
}

func storeDeploymentCustody(ctx context.Context, private deploymentPrivateData, value deploymentCustody) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	if private == nil {
		diagnostics.AddError(PO_PROVIDER_ERR, "Deployment private storage is unavailable; no mutation was attempted.")
		return diagnostics
	}
	validationErr := value.validate()
	raw, err := json.Marshal(value)
	if validationErr != nil || err != nil || len(raw) > deploymentPrivateLimit {
		diagnostics.AddError(PO_PROVIDER_ERR, "Unable to encode deployment custody; no private values were logged.")
		return diagnostics
	}
	// SetKey logs invalid JSON/UTF-8. Only typed, successfully marshalled values reach it.
	diagnostics.Append(private.SetKey(ctx, deploymentPrivateKey, raw)...)
	return diagnostics
}
func (operation *deploymentOperation) validate() error {
	if err := operation.validateMetadata(); err != nil {
		return err
	}
	body := operation.Request.Deployment
	if operation.OrgID == "" || body.ProjectId == "" || body.EnvId == "" || body.Manifest == nil {
		return deploymentPrivateError()
	}
	if body.Mode != dp.DeploymentCreateBodyModeDeploy && body.Mode != dp.DeploymentCreateBodyModePlanOnly {
		return deploymentPrivateError()
	}
	if operation.RequestHash != deploymentDigest(operation.Request) || operation.Intent != deploymentPreparedIntent(operation.OrgID, operation.Request) {
		return deploymentPrivateError()
	}
	return nil
}
func (d *DeploymentResource) validateDeploymentCustody(custody deploymentCustody, data DeploymentResourceModel) error {
	if custody.Current == nil && custody.Pending == nil {
		return nil
	}
	if d.scopeFingerprint == "" || d.credentialFingerprint == "" {
		return deploymentPrivateError()
	}
	for _, operation := range []*deploymentOperation{custody.Current, custody.Pending} {
		if operation == nil {
			continue
		}
		if operation.Scope != d.scopeFingerprint || operation.OrgID != d.orgId {
			return deploymentPrivateError()
		}
	}
	if custody.Current != nil && !custody.Current.matches(data.Id.ValueString(), d.orgId, data.ProjectId.ValueString(), data.EnvId.ValueString()) {
		return deploymentPrivateError()
	}
	return nil
}

func deploymentPreparedIntent(org string, request dp.ModuleTestDeploymentCreateBody) string {
	body := request.Deployment
	pipeline := ""
	if body.Pipeline != nil {
		pipeline = body.Pipeline.Id
	}
	body.Pipeline = nil
	body.EncryptedOutputsRecipient = nil
	return deploymentDigest(struct {
		Org, Pipeline string
		Body          dp.DeploymentCreateBody
		Tests         map[string]uuid.UUID
	}{org, pipeline, body, request.DraftTestSnapshots})
}
func (d *DeploymentResource) deploymentIntent(data DeploymentResourceModel) (string, error) {
	if data.WhenBusy.IsUnknown() || data.HaltOnFailure.IsUnknown() {
		return "", fmt.Errorf("deployment queue inputs must be known before admission")
	}
	var diagnostics diag.Diagnostics
	tests := validateDeploymentDraftTests(data.DraftTests, false, &diagnostics)
	if diagnostics.HasError() {
		return "", fmt.Errorf("invalid captured Module test selection")
	}
	body, err := d.deploymentBody(data)
	if err != nil {
		return "", err
	}
	if !data.PipelineVersionID.IsNull() {
		id, err := uuid.Parse(data.PipelineVersionID.ValueString())
		if err != nil || id == uuid.Nil {
			return "", fmt.Errorf("pipeline_version_id must be a published version UUID")
		}
		body.Pipeline = &dp.DeploymentPipelineSelection{Id: id.String()}
	}
	return deploymentPreparedIntent(d.orgId, dp.ModuleTestDeploymentCreateBody{Deployment: body, DraftTestSnapshots: tests}), nil
}
func (d *DeploymentResource) prepareDeployment(ctx context.Context, data *DeploymentResourceModel, custody deploymentCustody) (*deploymentOperation, error) {
	intent, err := d.deploymentIntent(*data)
	if err != nil {
		return nil, err
	}
	if custody.Pending != nil {
		pending := custody.Pending
		if pending.AcceptedID != "" {
			return nil, fmt.Errorf("a prior accepted deployment requires read-only recovery before another mutation")
		}
		if pending.Intent != intent || pending.Credential != d.credentialFingerprint {
			return nil, fmt.Errorf("pending deployment intent or credentials changed; recover the original operation before requesting another deployment")
		}
		return pending, nil
	}
	body, err := d.deploymentBody(*data)
	if err != nil {
		return nil, err
	}
	body.Pipeline, err = d.deploymentPipeline(ctx, data.PipelineVersionID, data.ProjectId.ValueString())
	if err != nil {
		return nil, err
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("unable to prepare deployment output custody")
	}
	recipient := identity.Recipient().String()
	body.EncryptedOutputsRecipient = &recipient
	var diagnostics diag.Diagnostics
	tests := validateDeploymentDraftTests(data.DraftTests, false, &diagnostics)
	if diagnostics.HasError() {
		return nil, fmt.Errorf("invalid captured Module test selection")
	}
	operation := &deploymentOperation{Intent: intent, Scope: d.scopeFingerprint, Credential: d.credentialFingerprint, RequestHash: deploymentDigest(dp.ModuleTestDeploymentCreateBody{Deployment: body, DraftTestSnapshots: tests}), RequestKey: uuid.NewString(), OutputsIdentity: identity.String(), OrgID: d.orgId, Request: dp.ModuleTestDeploymentCreateBody{Deployment: body, DraftTestSnapshots: tests}}
	if err := operation.validate(); err != nil {
		return nil, err
	}
	return operation, nil
}

func (operation *deploymentOperation) identity() (*age.X25519Identity, error) {
	identity, err := age.ParseX25519Identity(operation.OutputsIdentity)
	if err != nil || operation.Request.Deployment.EncryptedOutputsRecipient == nil {
		return nil, deploymentPrivateError()
	}
	if *operation.Request.Deployment.EncryptedOutputsRecipient != identity.Recipient().String() {
		return nil, deploymentPrivateError()
	}
	return identity, nil
}
func (operation *deploymentOperation) matches(id, org, project, env string) bool {
	return operation.AcceptedID == id && operation.OrgID == org && operation.Request.Deployment.ProjectId == project && operation.Request.Deployment.EnvId == env
}
func deploymentMatchesScope(deployment *dp.Deployment, org, project, env string) bool {
	return deployment != nil && deployment.Id != uuid.Nil && deployment.OrgId == org && deployment.ProjectId == project && deployment.EnvId == env
}

func (d *DeploymentResource) submitPreparedDeployment(ctx context.Context, operation *deploymentOperation) (*dp.CreateDeploymentResponse, error) {
	// Every mutation attempt must remain visible to custody handling. Disable
	// wrapper retries and Go's body replay, even with an Idempotency-Key header.
	ctx = retryhttp.SetMaxRetries(ctx, 0)
	noReplay := func(_ context.Context, request *http.Request) error {
		request.GetBody = nil
		return nil
	}
	params := &dp.CreateDeploymentParams{IdempotencyKey: &operation.RequestKey}
	if len(operation.Request.DraftTestSnapshots) > 0 {
		testParams := dp.CreateModuleTestDeploymentParams(*params)
		response, err := d.dpClient.CreateModuleTestDeploymentWithResponse(ctx, d.orgId, &testParams, operation.Request, noReplay)
		return (*dp.CreateDeploymentResponse)(response), err
	}
	return d.dpClient.CreateDeploymentWithResponse(ctx, d.orgId, params, operation.Request.Deployment, noReplay)
}

func nonzeroDeploymentUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil
}

// This classifies one un-retried wire refusal. It cannot release a previously
// ambiguous operation, even when a later attempt is refused.
func deploymentAdmissionRefused(status int, conflict *dp.N409Conflict) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
		return true
	case http.StatusConflict:
	default:
		return false
	}
	if conflict == nil {
		return false
	}
	switch conflict.Error {
	case "environment_busy", "deployment_queue_full", "outcome_unreconciled", "deployment_base_changed", "environment_deleting":
		return true
	default:
		return false
	}
}
func (operation *deploymentOperation) bindAdmission(deployment *dp.Deployment) error {
	if deployment.Id != uuid.Nil {
		operation.AcceptedID = deployment.Id.String()
	}
	body := operation.Request.Deployment
	if !deploymentMatchesScope(deployment, operation.OrgID, body.ProjectId, body.EnvId) {
		return fmt.Errorf("deployment admission identity or scope did not match its prepared request; no retry was attempted")
	}
	return nil
}

func (operation *deploymentOperation) validateMetadata() error {
	for _, digest := range []string{operation.Intent, operation.Scope, operation.Credential, operation.RequestHash} {
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != sha256.Size {
			return deploymentPrivateError()
		}
	}
	if !nonzeroDeploymentUUID(operation.RequestKey) {
		return deploymentPrivateError()
	}
	if operation.AcceptedID != "" && !nonzeroDeploymentUUID(operation.AcceptedID) {
		return deploymentPrivateError()
	}
	if _, err := operation.identity(); err != nil {
		return err
	}
	return nil
}
