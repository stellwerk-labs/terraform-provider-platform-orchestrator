package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

var _ datasource.DataSource = &PluginInstallationDataSource{}

func NewPluginInstallationDataSource() datasource.DataSource { return &PluginInstallationDataSource{} }

// PluginInstallationDataSource finds the installation of a plugin, so a
// configuration names the plugin instead of the UUID each Control Plane
// assigns at install time.
type PluginInstallationDataSource struct {
	cpClient cp.ClientWithResponsesInterface
}

type PluginInstallationDataSourceModel struct {
	PluginId       types.String `tfsdk:"plugin_id"`
	InstallationId types.String `tfsdk:"installation_id"`
	State          types.String `tfsdk:"state"`
	Availability   types.String `tfsdk:"availability"`
	PackageSha256  types.String `tfsdk:"package_sha256"`

	OrganizationMode     types.String               `tfsdk:"organization_mode"`
	DefaultTimezone      types.String               `tfsdk:"default_timezone"`
	DefaultPolicies      types.Map                  `tfsdk:"default_approval_policies"`
	Organizations        []PluginOrganizationModel  `tfsdk:"organizations"`
	PendingOrganizations []types.String             `tfsdk:"pending_organizations"`
	Licence              *PluginInstallationLicence `tfsdk:"licence"`
}

// PluginOrganizationModel is one enablement of the installation.
type PluginOrganizationModel struct {
	OrgId                  types.String             `tfsdk:"org_id"`
	State                  types.String             `tfsdk:"state"`
	Timezone               types.String             `tfsdk:"timezone"`
	ApprovalPolicies       types.Map                `tfsdk:"approval_policies"`
	CapabilityRegistration types.String             `tfsdk:"capability_registration"`
	LicenceGrant           *PluginLicenceGrantModel `tfsdk:"licence_grant"`
}

// PluginInstallationLicence is the licence of an installation whose package
// requires one.
type PluginInstallationLicence struct {
	EnabledOrganizations types.Int64              `tfsdk:"enabled_organizations"`
	InstallationGrant    *PluginLicenceGrantModel `tfsdk:"installation_grant"`
}

// PluginLicenceGrantModel is an imported grant as an operator sees it.
type PluginLicenceGrantModel struct {
	Scope             types.String   `tfsdk:"scope"`
	State             types.String   `tfsdk:"state"`
	TrustClass        types.String   `tfsdk:"trust_class"`
	Issuer            types.String   `tfsdk:"issuer"`
	Sequence          types.Int64    `tfsdk:"sequence"`
	Features          []types.String `tfsdk:"features"`
	OrganizationLimit types.Int64    `tfsdk:"organization_limit"`
	NotBefore         types.String   `tfsdk:"not_before"`
	ExpiresAt         types.String   `tfsdk:"expires_at"`
	GraceUntil        types.String   `tfsdk:"grace_until"`
}

func (d *PluginInstallationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugin_installation"
}

func (d *PluginInstallationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Finds the installation of a plugin in the Plugin Manager. A plugin has at most one installation that is not purged. " +
			"Installing and every other lifecycle step are Plugin Manager operator workflows in `v1alpha`, for example with `octl plugin-manager`. " +
			"The provider's token needs the platform permission `plugin_manager_read`.",
		Attributes: map[string]schema.Attribute{
			"plugin_id":       schema.StringAttribute{MarkdownDescription: "The plugin, for example `dev.stellwerk.progressive-rollouts`.", Required: true},
			"installation_id": schema.StringAttribute{MarkdownDescription: "The Core-assigned UUID of the installation.", Computed: true},
			"state":           schema.StringAttribute{MarkdownDescription: "`installed`, `active`, `disabled` or `uninstalled`.", Computed: true},
			"availability":    schema.StringAttribute{MarkdownDescription: "Derived from the state: `available` while active, `not_enabled` (safety-only) while disabled, `unavailable` while installed, `not_installed` once uninstalled.", Computed: true},
			"package_sha256":  schema.StringAttribute{MarkdownDescription: "The digest of the package Core serves.", Computed: true},
			"organization_mode": schema.StringAttribute{
				MarkdownDescription: "`selected` while a platform operator enables each organization, `all` while Core enables every organization, existing and created later, except those whose enablement is disabled. " +
					"Set the mode with `octl plugin-manager organization-mode`; the provider does not manage it.",
				Computed: true,
			},
			"default_timezone": schema.StringAttribute{MarkdownDescription: "The IANA timezone of every enablement Core creates on its own in mode `all`.", Computed: true},
			"default_approval_policies": schema.MapAttribute{
				MarkdownDescription: "Deployment approval scope to authorization policy of every enablement Core creates on its own in mode `all`.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"organizations": schema.ListNestedAttribute{
				MarkdownDescription: "Every organization the installation is enabled for, ordered by organization ID. A `disabled` enablement keeps the plugin safety-only for that organization and, in mode `all`, is the organization's opt-out.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"org_id":            schema.StringAttribute{MarkdownDescription: "The organization.", Computed: true},
					"state":             schema.StringAttribute{MarkdownDescription: "`enabled` or `disabled`.", Computed: true},
					"timezone":          schema.StringAttribute{MarkdownDescription: "The organization's IANA timezone for the plugin's calendar policies.", Computed: true},
					"approval_policies": schema.MapAttribute{MarkdownDescription: "Deployment approval scope to authorization policy of the organization.", ElementType: types.StringType, Computed: true},
					"capability_registration": schema.StringAttribute{
						MarkdownDescription: "Whether IAM holds the capability declarations of the served package for the organization: `registered`, `pending` until Core reconciled the served package, or `failed`, which Core retries.",
						Computed:            true,
					},
					"licence_grant": licenceGrantAttribute("The grant that covers the organization, its own or else the installation-wide grant. Null while none does or the package requires no licence."),
				}},
			},
			"pending_organizations": schema.ListAttribute{
				MarkdownDescription: "Only in mode `all`: the active organizations without an enablement, which Core could not enable when they were created because the plugin host was off or the installation-wide grant's organization limit was reached. " +
					"Running `octl plugin-manager organization-mode <installation> all` again enables them. Empty otherwise.",
				ElementType: types.StringType,
				Computed:    true,
			},
			"licence": schema.SingleNestedAttribute{
				MarkdownDescription: "Only when the served package requires a licence, otherwise null.",
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"enabled_organizations": schema.Int64Attribute{MarkdownDescription: "The enabled enablements of the installation, which the installation-wide grant's `organization_limit` caps.", Computed: true},
					"installation_grant":    licenceGrantAttribute("The installation-wide grant, which covers every organization without a grant of its own. Null while there is none."),
				},
			},
		},
	}
}

func licenceGrantAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		MarkdownDescription: description,
		Computed:            true,
		Attributes: map[string]schema.Attribute{
			"scope":              schema.StringAttribute{MarkdownDescription: "`organization` for a grant of the organization itself, which then decides alone, `installation` for an installation-wide grant.", Computed: true},
			"state":              schema.StringAttribute{MarkdownDescription: "`valid`, `grace`, `expired`, `not_yet_valid`, `revoked` or `untrusted` under the current issuer trust.", Computed: true},
			"trust_class":        schema.StringAttribute{MarkdownDescription: "`production` or `development`.", Computed: true},
			"issuer":             schema.StringAttribute{MarkdownDescription: "The issuer of the grant.", Computed: true},
			"sequence":           schema.Int64Attribute{MarkdownDescription: "The grant's sequence; a renewal has a higher one.", Computed: true},
			"features":           schema.ListAttribute{MarkdownDescription: "The licensed features.", ElementType: types.StringType, Computed: true},
			"organization_limit": schema.Int64Attribute{MarkdownDescription: "Only on an installation-wide grant that carries one: the number of organizations the installation may enable. Null otherwise.", Computed: true},
			"not_before":         schema.StringAttribute{MarkdownDescription: "RFC 3339 time the grant starts.", Computed: true},
			"expires_at":         schema.StringAttribute{MarkdownDescription: "RFC 3339 time the grant expires.", Computed: true},
			"grace_until":        schema.StringAttribute{MarkdownDescription: "RFC 3339 time the grace period after expiry ends.", Computed: true},
		},
	}
}

func (d *PluginInstallationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	providerData, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	d.cpClient = providerData.CpClient
}

func (d *PluginInstallationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data PluginInstallationDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := d.cpClient.ListPluginInstallationsWithResponse(ctx)
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to list plugin installations, got error: %s", err))
		return
	} else if list.StatusCode() != http.StatusOK || list.JSON200 == nil {
		addPluginManagerError(&resp.Diagnostics, "List plugin installations", list.StatusCode(), list.Body, nil)
		return
	}
	var found []cp.PluginInstallation
	for _, item := range list.JSON200.Items {
		if item.PluginId == data.PluginId.ValueString() && item.State != cp.PluginInstallationStatePurged {
			found = append(found, item)
		}
	}
	switch len(found) {
	case 0:
		resp.Diagnostics.AddError("Plugin installation not found", fmt.Sprintf("Plugin %q has no installation that is not purged.%s", data.PluginId.ValueString(), pluginInstallationNotFoundAdvice))
		return
	case 1:
	default:
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Plugin %q has %d installations that are not purged; expected at most one.", data.PluginId.ValueString(), len(found)))
		return
	}
	data.InstallationId = types.StringValue(found[0].InstallationId.String())
	data.State = types.StringValue(string(found[0].State))
	data.Availability = types.StringValue(string(found[0].Availability))
	data.PackageSha256 = types.StringValue(found[0].PackageSha256)
	resp.Diagnostics.Append(data.setOrganizations(ctx, found[0])...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// setOrganizations records the organization mode, the enablements, the
// pending organizations and the licence of the installation.
func (m *PluginInstallationDataSourceModel) setOrganizations(ctx context.Context, installation cp.PluginInstallation) diag.Diagnostics {
	var diags diag.Diagnostics
	m.OrganizationMode = types.StringValue(string(installation.OrganizationMode))
	m.DefaultTimezone = types.StringValue(installation.OrganizationDefaults.Timezone)
	var d diag.Diagnostics
	m.DefaultPolicies, d = types.MapValueFrom(ctx, types.StringType, installation.OrganizationDefaults.ApprovalPolicies)
	diags.Append(d...)
	m.Organizations = make([]PluginOrganizationModel, 0, len(installation.Organizations))
	for _, enablement := range installation.Organizations {
		organization := PluginOrganizationModel{
			OrgId: types.StringValue(enablement.OrgId), State: types.StringValue(string(enablement.State)), Timezone: types.StringValue(enablement.Timezone),
			CapabilityRegistration: types.StringValue(string(enablement.CapabilityRegistration.State)), LicenceGrant: licenceGrantModel(enablement.EntitlementGrant),
		}
		organization.ApprovalPolicies, d = types.MapValueFrom(ctx, types.StringType, enablement.ApprovalPolicies)
		diags.Append(d...)
		m.Organizations = append(m.Organizations, organization)
	}
	m.PendingOrganizations = []types.String{}
	if installation.PendingOrganizations != nil {
		for _, org := range *installation.PendingOrganizations {
			m.PendingOrganizations = append(m.PendingOrganizations, types.StringValue(org))
		}
	}
	m.Licence = nil
	if licence := installation.Entitlement; licence != nil {
		m.Licence = &PluginInstallationLicence{EnabledOrganizations: types.Int64Value(int64(licence.EnabledOrganizations)), InstallationGrant: licenceGrantModel(licence.InstallationGrant)}
	}
	return diags
}

func licenceGrantModel(grant *cp.PluginEntitlementGrant) *PluginLicenceGrantModel {
	if grant == nil {
		return nil
	}
	model := &PluginLicenceGrantModel{
		Scope: types.StringValue(string(grant.Scope)), State: types.StringValue(string(grant.State)), TrustClass: types.StringValue(string(grant.TrustClass)),
		Issuer: types.StringValue(grant.Issuer), Sequence: types.Int64Value(grant.Sequence), Features: make([]types.String, 0, len(grant.Features)),
		OrganizationLimit: types.Int64PointerValue(grant.OrganizationLimit),
		NotBefore:         types.StringValue(grant.NotBefore.Format(time.RFC3339)), ExpiresAt: types.StringValue(grant.ExpiresAt.Format(time.RFC3339)), GraceUntil: types.StringValue(grant.GraceUntil.Format(time.RFC3339)),
	}
	for _, feature := range grant.Features {
		model.Features = append(model.Features, types.StringValue(feature))
	}
	return model
}
