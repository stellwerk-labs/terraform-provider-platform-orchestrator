package provider

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"regexp"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

var _ resource.Resource = &PluginEnablementResource{}
var _ resource.ResourceWithImportState = &PluginEnablementResource{}

// installationIDPattern is the canonical form the Plugin Manager reports, so
// state always holds the configured value.
var installationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func NewPluginEnablementResource() resource.Resource { return &PluginEnablementResource{} }

// PluginEnablementResource is the organization enablement of a plugin
// installation. It exists while the organization is enabled; destroying it
// disables the organization and never removes the enablement.
type PluginEnablementResource struct {
	cpClient cp.ClientWithResponsesInterface
	orgId    string
}

type PluginEnablementModel struct {
	InstallationId   types.String `tfsdk:"installation_id"`
	PluginId         types.String `tfsdk:"plugin_id"`
	Reason           types.String `tfsdk:"reason"`
	Timezone         types.String `tfsdk:"timezone"`
	ApprovalPolicies types.Map    `tfsdk:"approval_policies"`
}

func (r *PluginEnablementResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugin_enablement"
}

func (r *PluginEnablementResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Enables a plugin installation for the provider's organization: the Plugin Manager decision a platform team manages declaratively.\n\n" +
			"Creating the resource enables the organization. Destroying it disables the organization, which keeps the plugin safety-only: " +
			"cancel, rollback and reconciliation of plugin work in flight stay permitted, new work is refused. " +
			"The enablement record with its timezone and approval policies stays; Terraform never removes it, because Core refuses even safety work of a plugin without one. " +
			"An organization disabled outside Terraform is enabled again by the next apply.\n\n" +
			"Every change is a Plugin Manager command: the provider reads the installation, sends the Plugin Manager revision it read as `expected_revision` together with `reason`, " +
			"and decides again on a fresh read when another change landed in between. A change whose target already holds writes nothing.\n\n" +
			"The installation must be active, or disabled installation-wide, before an organization can be enabled. An installation serves every organization it is enabled for, each with its own state, timezone and approval policies, " +
			"and its identities belong to no organization, so any organization can be enabled. When the installation-wide licence grant limits the enabled organizations, enabling one beyond the limit is refused with `plugin_organization_limit`.\n\n" +
			"While the installation serves all organizations (`organization_mode` `all`, set by a platform operator with `octl plugin-manager organization-mode`), Core enables every organization itself, " +
			"so the resource adopts the enablement Core created: creating it writes nothing when its settings already hold, and settings you configure replace the defaults. " +
			"Destroying it disables the organization, which in that mode is the organization's opt-out: Core never enables it again, not when the mode is set again and not for a new organization. Removing the enablement is refused in that mode, which is why destroy disables. " +
			"The mode itself is not managed by the provider.\n\n" +
			"The rest of the installation lifecycle (register the package, install, identities, activate, upgrade, disable, uninstall, reinstall, purge, organization mode) and licence imports stay Plugin Manager operator workflows in `v1alpha`, " +
			"for example with `octl plugin-manager`. The provider's token needs the platform permissions `plugin_manager_read` and `plugin_manager_write`.",
		Attributes: map[string]schema.Attribute{
			"installation_id": schema.StringAttribute{
				MarkdownDescription: "The Core-assigned UUID of the plugin installation, for example from the `platform-orchestrator_plugin_installation` data source. " +
					"Changing it disables the organization for the old installation and enables it for the new one.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.RegexMatches(installationIDPattern, "must be a lowercase UUID")},
			},
			"plugin_id": schema.StringAttribute{
				MarkdownDescription: "The plugin the installation belongs to.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"reason": schema.StringAttribute{
				MarkdownDescription: "Why the organization is enabled, at most 1000 characters. The Plugin Manager records it in the audit event of every change the resource makes, " +
					"including the disable on destroy. Changing only the reason writes nothing; it is recorded with the next change. Import leaves it empty until the next apply.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 1000),
					stringvalidator.RegexMatches(regexp.MustCompile(`\S`), "must not be blank"),
				},
			},
			"timezone": schema.StringAttribute{
				MarkdownDescription: "The organization's IANA timezone for the plugin's calendar policies, for example `Europe/Berlin`. Unset keeps the current setting, `UTC` for a new enablement.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"approval_policies": schema.MapAttribute{
				MarkdownDescription: "Deployment approval scope (`environment` or `project`) to the authorization policy of the plugin package that approves deployments in that scope. " +
					"Unset keeps the current setting, none for a new enablement; `{}` removes all.",
				ElementType:   types.StringType,
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
				Validators:    []validator.Map{mapvalidator.KeysAre(stringvalidator.OneOf("environment", "project"))},
			},
		},
	}
}

func (r *PluginEnablementResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	providerData, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	r.cpClient = providerData.CpClient
	r.orgId = providerData.OrgId
}

func (r *PluginEnablementResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PluginEnablementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.enable(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PluginEnablementResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PluginEnablementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := uuid.Parse(data.InstallationId.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, fmt.Sprintf("installation_id must be a UUID: %s", err))
		return
	}
	installation, found := r.readInstallation(ctx, id, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var enablement *cp.PluginEnablement
	if found {
		enablement = enablementOf(*installation, r.orgId)
	}
	if enablement == nil || enablement.State != cp.PluginEnablementStateEnabled {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(data.set(ctx, *installation, *enablement)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PluginEnablementResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data PluginEnablementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.enable(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Delete disables the organization and keeps its settings. An installation
// that is gone or no longer enables the organization needs nothing.
func (r *PluginEnablementResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PluginEnablementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	reason := data.Reason.ValueString()
	if reason == "" {
		reason = "The plugin enablement was removed from the Terraform configuration."
	}
	r.putEnablement(ctx, data.InstallationId.ValueString(), true, &resp.Diagnostics, disableEnablement(reason))
}

// disableEnablement disables an enabled organization and keeps its settings.
func disableEnablement(reason string) func(current *cp.PluginEnablement) *cp.PluginEnablementPut {
	return func(current *cp.PluginEnablement) *cp.PluginEnablementPut {
		if current == nil || current.State != cp.PluginEnablementStateEnabled {
			return nil
		}
		return &cp.PluginEnablementPut{Reason: reason, State: cp.PluginEnablementPutStateDisabled, Timezone: &current.Timezone, ApprovalPolicies: &current.ApprovalPolicies}
	}
}

func (r *PluginEnablementResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := uuid.Parse(req.ID); err != nil {
		resp.Diagnostics.AddError(PO_INPUT_ERR, fmt.Sprintf("Import the enablement by its installation UUID, got %q: %s", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("installation_id"), req.ID)...)
}

// enable converges the organization's enablement to enabled with the planned
// settings and records the result in data. Settings the plan leaves unknown
// keep what the enablement has.
func (r *PluginEnablementResource) enable(ctx context.Context, data *PluginEnablementModel, diags *diag.Diagnostics) {
	timezone := fromStringValueToStringPointer(data.Timezone)
	var policies *map[string]string
	if !data.ApprovalPolicies.IsUnknown() && !data.ApprovalPolicies.IsNull() {
		policies = &map[string]string{}
		diags.Append(data.ApprovalPolicies.ElementsAs(ctx, policies, false)...)
		if diags.HasError() {
			return
		}
	}
	installation := r.putEnablement(ctx, data.InstallationId.ValueString(), false, diags, func(current *cp.PluginEnablement) *cp.PluginEnablementPut {
		body := cp.PluginEnablementPut{Reason: data.Reason.ValueString(), State: cp.PluginEnablementPutStateEnabled, Timezone: timezone, ApprovalPolicies: policies}
		if current == nil {
			return &body
		}
		if body.Timezone == nil {
			body.Timezone = &current.Timezone
		}
		if body.ApprovalPolicies == nil {
			body.ApprovalPolicies = &current.ApprovalPolicies
		}
		if current.State == cp.PluginEnablementStateEnabled && *body.Timezone == current.Timezone && maps.Equal(*body.ApprovalPolicies, current.ApprovalPolicies) {
			return nil
		}
		return &body
	})
	if installation == nil {
		return
	}
	enablement := enablementOf(*installation, r.orgId)
	if enablement == nil || enablement.State != cp.PluginEnablementStateEnabled {
		diags.AddError(PO_API_ERR, fmt.Sprintf("Installation %s does not report organization %s as enabled after enabling it.", installation.InstallationId, r.orgId))
		return
	}
	diags.Append(data.set(ctx, *installation, *enablement)...)
	if installation.State == cp.PluginInstallationStateDisabled {
		diags.AddWarning("Plugin installation disabled",
			fmt.Sprintf("Organization %s is enabled for installation %s of plugin %s, but the installation is disabled for every organization: the plugin stays safety-only until a platform operator enables the installation again (octl plugin-manager enable %s).",
				r.orgId, installation.InstallationId, installation.PluginId, installation.InstallationId))
	}
}

// putEnablement converges the organization's enablement of an installation.
// target derives the request from the enablement as read, nil when the
// organization has none, and returns nil when nothing is to change. Another
// change landing between read and write makes it read and decide again. It
// returns the installation as of the last read or write, or nil when that
// failed or, with missingOK, when the installation does not exist.
func (r *PluginEnablementResource) putEnablement(ctx context.Context, installationID string, missingOK bool, diags *diag.Diagnostics, target func(current *cp.PluginEnablement) *cp.PluginEnablementPut) *cp.PluginInstallation {
	id, err := uuid.Parse(installationID)
	if err != nil {
		diags.AddError(PO_INPUT_ERR, fmt.Sprintf("installation_id must be a UUID: %s", err))
		return nil
	}
	action := fmt.Sprintf("Change the enablement of organization %s for plugin installation %s", r.orgId, id)
	for attempt := 1; ; attempt++ {
		installation, found := r.readInstallation(ctx, id, diags)
		if diags.HasError() || (!found && missingOK) {
			return nil
		} else if !found {
			diags.AddError("Plugin installation not found", fmt.Sprintf("Plugin installation %s does not exist.%s", id, pluginInstallationNotFoundAdvice))
			return nil
		}
		body := target(enablementOf(*installation, r.orgId))
		if body == nil {
			return installation
		}
		if installation.Revision == nil {
			diags.AddError(PO_API_ERR, fmt.Sprintf("Reading plugin installation %s did not report the Plugin Manager revision.", id))
			return nil
		}
		body.ExpectedRevision = *installation.Revision
		key := pluginManagerIdempotencyKey(fmt.Sprintf("/plugin-installations/%s/organizations/%s", id, r.orgId), *body)
		put, err := r.cpClient.PutPluginEnablementWithResponse(ctx, id, r.orgId, &cp.PutPluginEnablementParams{IdempotencyKey: key}, *body)
		switch {
		case err != nil:
			diags.AddError(PO_CLIENT_ERR, fmt.Sprintf("%s, got error: %s", action, err))
			return nil
		case put.StatusCode() == http.StatusOK && put.JSON200 != nil:
			tflog.Info(ctx, "Plugin enablement changed", map[string]any{"installation_id": id.String(), "org_id": r.orgId, "state": string(body.State), "revision": body.ExpectedRevision + 1})
			return put.JSON200
		case put.JSON409 != nil && put.JSON409.Error == pluginManagerRevisionConflict && attempt < pluginManagerWriteAttempts:
			tflog.Info(ctx, "Plugin Manager changed meanwhile, deciding again", map[string]any{"installation_id": id.String(), "expected_revision": body.ExpectedRevision, "attempt": attempt})
		default:
			addPluginManagerError(diags, action, put.StatusCode(), put.Body, installation)
			return nil
		}
	}
}

// readInstallation reads an installation. found is false when it does not
// exist; every other failure is a diagnostic.
func (r *PluginEnablementResource) readInstallation(ctx context.Context, id uuid.UUID, diags *diag.Diagnostics) (installation *cp.PluginInstallation, found bool) {
	got, err := r.cpClient.GetPluginInstallationWithResponse(ctx, id)
	switch {
	case err != nil:
		diags.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to read plugin installation %s, got error: %s", id, err))
	case got.StatusCode() == http.StatusOK && got.JSON200 != nil:
		return got.JSON200, true
	case got.JSON404 != nil && got.JSON404.Error == "plugin_installation_not_found":
	default:
		addPluginManagerError(diags, "Read plugin installation "+id.String(), got.StatusCode(), got.Body, nil)
	}
	return nil, false
}

func (m *PluginEnablementModel) set(ctx context.Context, installation cp.PluginInstallation, enablement cp.PluginEnablement) diag.Diagnostics {
	m.InstallationId = types.StringValue(installation.InstallationId.String())
	m.PluginId = types.StringValue(installation.PluginId)
	m.Timezone = types.StringValue(enablement.Timezone)
	var diags diag.Diagnostics
	m.ApprovalPolicies, diags = types.MapValueFrom(ctx, types.StringType, enablement.ApprovalPolicies)
	return diags
}
