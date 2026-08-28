package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
)

var _ datasource.DataSource = &MetadataKeyDataSource{}

func NewMetadataKeyDataSource() datasource.DataSource { return &MetadataKeyDataSource{} }

type MetadataKeyDataSource struct {
	dpClient dp.ClientWithResponsesInterface
	orgId    string
}

func metadataKeyDataSourceAttributes(nameRequired bool) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"name":        schema.StringAttribute{MarkdownDescription: "The metadata key name.", Required: nameRequired, Computed: !nameRequired},
		"description": schema.StringAttribute{MarkdownDescription: "The human-readable description.", Computed: true},
		"schema_type": schema.StringAttribute{MarkdownDescription: "The metadata value type.", Computed: true},
		"format":      schema.StringAttribute{MarkdownDescription: "The optional string format constraint.", Computed: true},
		"pattern":     schema.StringAttribute{MarkdownDescription: "The optional regular-expression constraint.", Computed: true},
		"created_at":  schema.StringAttribute{MarkdownDescription: "The time the metadata key was created.", Computed: true},
	}
}

func (d *MetadataKeyDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_metadata_key"
}

func (d *MetadataKeyDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Looks up an organization metadata key by name.", Attributes: metadataKeyDataSourceAttributes(true)}
}

func (d *MetadataKeyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	providerData, ok := req.ProviderData.(*PlatformOrchestratorProviderData)
	if !ok {
		resp.Diagnostics.AddError(PO_PROVIDER_ERR, fmt.Sprintf("Expected *PlatformOrchestratorProviderData, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	d.dpClient = providerData.DpClient
	d.orgId = providerData.OrgId
}

func (d *MetadataKeyDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data MetadataKeyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	httpResp, err := d.dpClient.GetMetadataKeyWithResponse(ctx, d.orgId, data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to read metadata key: %s", err))
		return
	}
	if httpResp.StatusCode() == http.StatusNotFound {
		resp.Diagnostics.AddError(PO_RESOURCE_NOT_FOUND_ERR, fmt.Sprintf("Metadata key %q not found in org %s", data.Name.ValueString(), d.orgId))
		return
	}
	if httpResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to read metadata key, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, toMetadataKeyModel(*httpResp.JSON200))...)
}
