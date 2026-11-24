package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func (d *DeploymentResource) readDeploymentOutputs(ctx context.Context, id uuid.UUID, identity *age.X25519Identity, data *DeploymentResourceModel, diagnostics *diag.Diagnostics) {
	response, err := d.dpClient.GetDeploymentEncryptedOutputsWithResponse(ctx, d.orgId, id)
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, "Unable to read encrypted deployment outputs; retained custody and existing outputs were preserved.")
		return
	}
	if response.StatusCode() != 200 || response.JSON200 == nil {
		diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to read encrypted deployment outputs (HTTP %d); retained custody and existing outputs were preserved.", response.StatusCode()))
		return
	}
	plaintext, err := age.Decrypt(base64.NewDecoder(base64.StdEncoding, strings.NewReader(response.JSON200.Raw)), identity)
	if err != nil {
		diagnostics.AddError(PO_PROVIDER_ERR, "Unable to decrypt deployment outputs with retained custody; no new deployment was requested.")
		return
	}
	raw, err := io.ReadAll(plaintext)
	if err != nil {
		diagnostics.AddError(PO_PROVIDER_ERR, "Unable to read decrypted deployment outputs; existing outputs were preserved.")
		return
	}
	data.Outputs = types.StringValue(string(raw))
}

func (d *DeploymentResource) recoverDeploymentOutputs(ctx context.Context, operation *deploymentOperation, data *DeploymentResourceModel, diagnostics *diag.Diagnostics) {
	if operation == nil {
		if data.Outputs.IsNull() {
			diagnostics.AddWarning("Deployment outputs unavailable", "This historical deployment has no retained output identity. Existing infrastructure was not reapplied.")
		}
		return
	}
	if !operation.matches(data.Id.ValueString(), d.orgId, data.ProjectId.ValueString(), data.EnvId.ValueString()) || operation.Scope != d.scopeFingerprint {
		diagnostics.AddError(PO_PROVIDER_ERR, "Retained outputs custody does not match this deployment.")
		return
	}
	identity, err := operation.identity()
	if err != nil {
		diagnostics.AddError(PO_PROVIDER_ERR, "Retained deployment outputs custody is invalid.")
		return
	}
	id, err := uuid.Parse(operation.AcceptedID)
	if err != nil {
		diagnostics.AddError(PO_PROVIDER_ERR, "Retained deployment identity is invalid.")
		return
	}
	d.readDeploymentOutputs(ctx, id, identity, data, diagnostics)
}
