package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
)

var _ datasource.DataSource = &MetadataKeysDataSource{}

func NewMetadataKeysDataSource() datasource.DataSource { return &MetadataKeysDataSource{} }

type MetadataKeysDataSource struct {
	dpClient dp.ClientWithResponsesInterface
	orgId    string
}

type MetadataKeysDataSourceModel struct {
	MetadataKeys types.List `tfsdk:"metadata_keys"`
}

func metadataKeyObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"name": types.StringType, "description": types.StringType, "schema_type": types.StringType,
		"format": types.StringType, "pattern": types.StringType, "created_at": types.StringType,
	}}
}

func (d *MetadataKeysDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_metadata_keys"
}

func (d *MetadataKeysDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Lists all organization metadata keys.", Attributes: map[string]schema.Attribute{
		"metadata_keys": schema.ListNestedAttribute{MarkdownDescription: "The metadata keys.", Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: metadataKeyDataSourceAttributes(false)}},
	}}
}

func (d *MetadataKeysDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *MetadataKeysDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data MetadataKeysDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var keys []attr.Value
	var page *string
	for {
		httpResp, err := d.dpClient.ListMetadataKeysWithResponse(ctx, d.orgId, &dp.ListMetadataKeysParams{Page: page})
		if err != nil {
			resp.Diagnostics.AddError(PO_CLIENT_ERR, fmt.Sprintf("Unable to list metadata keys: %s", err))
			return
		}
		if httpResp.StatusCode() != http.StatusOK {
			resp.Diagnostics.AddError(PO_API_ERR, fmt.Sprintf("Unable to list metadata keys, unexpected status code: %d, body: %s", httpResp.StatusCode(), httpResp.Body))
			return
		}
		for _, key := range httpResp.JSON200.Items {
			value, diags := types.ObjectValueFrom(ctx, metadataKeyObjectType().AttrTypes, toMetadataKeyModel(key))
			resp.Diagnostics.Append(diags...)
			if resp.Diagnostics.HasError() {
				return
			}
			keys = append(keys, value)
		}
		page = httpResp.JSON200.NextPageToken
		if page == nil || *page == "" {
			break
		}
	}
	data.MetadataKeys = types.ListValueMust(metadataKeyObjectType(), keys)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
