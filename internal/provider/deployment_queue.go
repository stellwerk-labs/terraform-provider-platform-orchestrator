package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
	"github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/ref"
)

const (
	whenBusyQueue  = "queue"
	whenBusyReject = "reject"
)

// deploymentQueuePollInterval is how often a queued deployment request is read
// again while it waits for its Environment.
var deploymentQueuePollInterval = 5 * time.Second

// queueCancelTimeout bounds the cancellation of a queued request after the
// create or update timeout elapsed.
const queueCancelTimeout = 30 * time.Second

// addDeploymentConflictError reports a refused deployment with its stable Data
// Plane code and what to do next. sent is the request's when_busy, nil for
// plan_only; fenced explains outcome_unreconciled.
func addDeploymentConflictError(diagnostics *diag.Diagnostics, sent *dp.DeploymentCreateBodyWhenBusy, conflict *dp.N409Conflict, body []byte, fenced string) {
	if conflict == nil {
		diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to create deployment, conflict: %s", body))
		return
	}
	detail := fmt.Sprintf("The deployment was refused (%s): %s.", conflict.Error, conflict.Message)
	switch conflict.Error {
	case "environment_busy":
		switch {
		case sent == nil:
			detail += " plan_only deployments never wait in the environment's deployment queue and do not run beside another deployment of the environment; apply again once it finished."
		case *sent == dp.Reject:
			detail += ` Another deployment is running or waiting. Remove when_busy or set when_busy = "queue" (the default) to wait in the environment's deployment queue within the create or update timeout.`
		default:
			detail += " Requests authorized by an organization token cannot wait in the deployment queue. Configure the provider with the token of a user or service user to wait, or apply again once the environment is free."
		}
		diagnostics.AddError("Environment busy", detail)
	case "deployment_queue_full":
		diagnostics.AddError("Deployment queue full", detail+" Wait until queued deployments of the environment finished, or cancel obsolete ones, and apply again.")
	case "outcome_unreconciled":
		diagnostics.AddError("Deployment outcome unreconciled", detail+fenced)
	case "deployment_base_changed":
		diagnostics.AddError("Deployment base changed", detail+" Another deployment changed the environment; refresh and apply again.")
	case "environment_deleting":
		diagnostics.AddError("Environment deleting", detail)
	default:
		diagnostics.AddError(PO_API_ERR, detail)
	}
}

// addPendingChangesWarning says why a plan_only deployment may not describe
// what a later deploy does: deployments that run or wait in the environment's
// deployment queue change the environment first.
func addPendingChangesWarning(diagnostics *diag.Diagnostics, pending *dp.PendingEnvironmentChanges) {
	if pending == nil {
		return
	}
	running := "Deployments"
	if pending.Running == 1 {
		running = "Deployment"
	}
	diagnostics.AddWarning("Plan result may change", fmt.Sprintf("The result may change: %d %s running, %d queued (%s). The plan_only deployment describes the environment as it is now, and those deployments change it first.",
		pending.Running, running, pending.Queued, pending.Code))
}

// environmentFenceDetail explains an outcome_unreconciled refusal with the
// Environment fence the Data Plane reports on the Environment's last
// deployment. A failed lookup only loses the detail, never the refusal.
func (d *DeploymentResource) environmentFenceDetail(ctx context.Context, projectID, envID string) string {
	r, err := d.dpClient.ListLastDeploymentsWithResponse(ctx, d.orgId, &dp.ListLastDeploymentsParams{ProjectId: &projectID, EnvId: &envID})
	if err != nil || r.StatusCode() != http.StatusOK || r.JSON200 == nil {
		cause := fmt.Sprint(err)
		if err == nil {
			cause = fmt.Sprintf("HTTP %d: %s", r.StatusCode(), r.Body)
		}
		tflog.Warn(ctx, "Unable to read the environment fence", map[string]any{"project_id": projectID, "env_id": envID, "error": cause})
		return " A failed plugin-dispatched deployment may still be changing the environment. Once an operator confirmed that its Runner can no longer change it and acknowledged its outcome, apply again."
	}
	for _, item := range r.JSON200.Items {
		if item.ProjectId == projectID && item.EnvId == envID && item.EnvironmentFence != nil {
			return describeEnvironmentFence(d.orgId, *item.EnvironmentFence)
		}
	}
	return " The environment is no longer fenced: the outcome was acknowledged or its Runner reported. Apply again."
}

// describeEnvironmentFence names the deployment that fences the Environment
// and the operator action that lifts the fence.
func describeEnvironmentFence(orgID string, fence dp.EnvironmentFence) string {
	detail := fmt.Sprintf(" The environment is fenced since %s by failed plugin-dispatched deployment %s: its Runner may still be changing the environment, so every new deployment is refused. Once an operator confirmed that the Runner can no longer change it, they lift the fence with the %s action of deployment %s (POST /internal/orgs/%s/deployments/%s/actions/%s)",
		fence.Since.UTC().Format(time.RFC3339), fence.DeploymentId, fence.Remedy, fence.DeploymentId, orgID, fence.DeploymentId, fence.Remedy)
	if fence.RemedyAvailableAt != nil {
		detail += fmt.Sprintf(", accepted from %s when the Runner dispatch window closes", fence.RemedyAvailableAt.UTC().Format(time.RFC3339))
	}
	return detail + ". Acknowledging starts no deployment; apply again afterwards."
}

// awaitQueuedDeployment follows a queued request until it became a Deployment
// and returns that Deployment. When ctx ends first, the request is cancelled so
// that no deployment Terraform does not track starts later.
func (d *DeploymentResource) awaitQueuedDeployment(ctx context.Context, item dp.DeploymentQueueItem, diagnostics *diag.Diagnostics) *dp.Deployment {
	tflog.Info(ctx, "Environment busy, deployment request waits in the deployment queue", map[string]any{
		"queue_item_id": item.Id.String(), "position": item.Position, "expires_at": item.ExpiresAt.Format(time.RFC3339),
	})
	ticker := time.NewTicker(deploymentQueuePollInterval)
	defer ticker.Stop()
	for item.State == dp.Queued {
		select {
		case <-ctx.Done():
			return d.abandonQueuedDeployment(ctx, item, diagnostics)
		case <-ticker.C:
		}
		r, err := d.dpClient.GetDeploymentQueueItemWithResponse(ctx, d.orgId, item.Id)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to read deployment queue item %s, got error: %s", item.Id, err))
			return nil
		} else if r.StatusCode() != http.StatusOK || r.JSON200 == nil {
			addAPIResponseError(diagnostics, "Read deployment queue item "+item.Id.String(), r.StatusCode(), r.Body)
			return nil
		}
		if r.JSON200.State == dp.Queued && (r.JSON200.Position != item.Position || r.JSON200.Attempts != item.Attempts) {
			tflog.Info(ctx, "Deployment request still queued", map[string]any{
				"queue_item_id": item.Id.String(), "position": r.JSON200.Position, "attempts": r.JSON200.Attempts, "last_error": ref.DerefOr(r.JSON200.LastError, ""),
			})
		}
		item = *r.JSON200
	}
	if item.State == dp.Dispatched && item.DeploymentId != nil {
		tflog.Info(ctx, "Queued deployment request started", map[string]any{"queue_item_id": item.Id.String(), "deployment_id": item.DeploymentId.String()})
		return d.getDeployment(ctx, item, diagnostics)
	}
	reason := ref.DerefOr(item.ReasonCode, string(item.State))
	detail := fmt.Sprintf("The deployment request waited in the environment's deployment queue as item %s and ended %s (%s): %s.", item.Id, item.State, reason, ref.DerefOr(item.ReasonMessage, ""))
	switch {
	case reason == "previous_deployment_failed":
		failed := "the deployment it waited for"
		if item.ReasonDeploymentId != nil {
			failed = "deployment " + item.ReasonDeploymentId.String()
		}
		diagnostics.AddError("Deployment halted after a failure", detail+fmt.Sprintf(" halt_on_failure is set, so the deployment did not start after %s failed. Find out why it failed, fix the cause and apply again.", failed))
		return nil
	case reason == "deployment_base_changed":
		detail += " Another deployment changed the environment; refresh and apply again."
	case reason == string(dp.OutcomeUnreconciled):
		detail += d.environmentFenceDetail(ctx, item.ProjectId, item.EnvId)
	case item.State == dp.Expired:
		detail += " The request expired before the environment became free; apply again."
	}
	diagnostics.AddError("Queued deployment did not start", detail)
	return nil
}

// abandonQueuedDeployment cancels a request whose wait outlived the timeout. A
// cancellation can lose the race against promotion; a request that started
// anyway is returned so that state records the running Deployment.
func (d *DeploymentResource) abandonQueuedDeployment(ctx context.Context, item dp.DeploymentQueueItem, diagnostics *diag.Diagnostics) *dp.Deployment {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), queueCancelTimeout)
	defer cancel()
	reason := "Terraform stopped waiting: the create or update timeout elapsed"
	if r, err := d.dpClient.CancelDeploymentQueueItemWithResponse(cleanup, d.orgId, item.Id, dp.DeploymentQueueCancelBody{Reason: &reason}); err == nil && r.StatusCode() == http.StatusOK && r.JSON200 != nil && r.JSON200.State == dp.Cancelled {
		diagnostics.AddError("Timed out in the deployment queue", fmt.Sprintf("The deployment request waited in the environment's deployment queue until the timeout elapsed and was cancelled (queue item %s). Increase the create or update timeout to wait longer.", item.Id))
		return nil
	}
	if r, err := d.dpClient.GetDeploymentQueueItemWithResponse(cleanup, d.orgId, item.Id); err == nil && r.StatusCode() == http.StatusOK && r.JSON200 != nil && r.JSON200.State == dp.Dispatched && r.JSON200.DeploymentId != nil {
		diagnostics.AddError("Timed out as the queued deployment started", fmt.Sprintf("Queue item %s started deployment %s as the timeout elapsed. The deployment is recorded in state but its completion was not awaited.", item.Id, *r.JSON200.DeploymentId))
		return d.getDeployment(cleanup, *r.JSON200, diagnostics)
	}
	diagnostics.AddError("Timed out in the deployment queue", fmt.Sprintf("The deployment request waited in the environment's deployment queue until the timeout elapsed and could not be cancelled. Queue item %s may still start a deployment that Terraform does not track.", item.Id))
	return nil
}

func (d *DeploymentResource) getDeployment(ctx context.Context, item dp.DeploymentQueueItem, diagnostics *diag.Diagnostics) *dp.Deployment {
	r, err := d.dpClient.GetDeploymentWithResponse(ctx, d.orgId, *item.DeploymentId)
	if err != nil {
		diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to read deployment %s started by queue item %s, got error: %s", *item.DeploymentId, item.Id, err))
		return nil
	} else if r.StatusCode() != http.StatusOK || r.JSON200 == nil {
		addAPIResponseError(diagnostics, "Read deployment "+item.DeploymentId.String(), r.StatusCode(), r.Body)
		return nil
	}
	return r.JSON200
}
