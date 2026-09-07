package provider

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

func retainedDependencyPolicyAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true, Computed: true, Default: stringdefault.StaticString("delete"),
		Validators:          []validator.String{stringvalidator.OneOf("delete", "retain")},
		MarkdownDescription: "Destroy behavior: delete (default) requests API deletion and reports references that block it; retain removes only Terraform ownership without modifying the API resource. Select retain explicitly for dependencies of permanently retained Module history. Import retained identities before managing them again.",
	}
}
