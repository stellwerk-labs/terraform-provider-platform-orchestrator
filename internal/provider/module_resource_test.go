package provider

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccLegacyModuleResourceMigrationDiagnostic(t *testing.T) {
	for _, source := range []struct {
		name string
		hcl  string
	}{
		{name: "external", hcl: `module_source = "git::https://example.com/modules/database.git?ref=release"`},
		{name: "inline", hcl: `module_source = "inline"
module_source_code = <<EOT
output "value" { value = "database" }
EOT
`},
	} {
		t.Run(source.name, func(t *testing.T) {
			id := fmt.Sprintf("legacy-write-%d", time.Now().UnixNano())
			resource.Test(t, resource.TestCase{
				PreCheck: func() { testAccPreCheck(t) }, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: fmt.Sprintf(`
resource "platform-orchestrator_resource_type" "legacy" {
  id = %[1]q
  output_schema = jsonencode({})
}
resource "platform-orchestrator_module" "legacy" {
  id = %[1]q
  resource_type = platform-orchestrator_resource_type.legacy.id
  %[2]s
}
`, id, source.hcl),
					ExpectError: regexp.MustCompile("Legacy Module authoring is unavailable"),
				}},
			})
		})
	}
}
