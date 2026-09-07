package provider

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestAccResourceTypeResource(t *testing.T) {
	var resourceTypeId = fmt.Sprintf("aws-rds-%d", time.Now().UnixNano())
	outputSchema := `{
		"properties": {
			"host": {
				"type": "string"
			},
			"port": {
				"type": "integer"
			}
		},
		"type": "object"
	}`
	description := "Example Resource Type"
	isDeveloperAccessibleFalse := false

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccResourceTypeResourceConfig(resourceTypeId, "{}", nil, nil),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"platform-orchestrator_resource_type.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(resourceTypeId),
					),
					statecheck.ExpectKnownValue(
						"platform-orchestrator_resource_type.test",
						tfjsonpath.New("description"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"platform-orchestrator_resource_type.test",
						tfjsonpath.New("output_schema"),
						knownvalue.StringExact("{}"),
					),
					statecheck.ExpectKnownValue(
						"platform-orchestrator_resource_type.test",
						tfjsonpath.New("is_developer_accessible"),
						knownvalue.Bool(true),
					),
				},
			},
			// Published contracts cannot be replaced under the same identity.
			{
				Config:      testAccResourceTypeResourceConfig(resourceTypeId, outputSchema, &description, &isDeveloperAccessibleFalse),
				ExpectError: regexp.MustCompile("Resource Type contract is immutable"),
			},
			{Config: testAccResourceTypeResourceConfig(resourceTypeId, "{}", nil, nil)},
			{
				ResourceName: "platform-orchestrator_resource_type.test",
				ImportState:  true,
			},
			// Delete testing automatically occurs in TestCase
		},
	})
}

func testAccResourceTypeResourceConfig(id, outputSchema string, description *string, isDeveloperAccessible *bool) string {
	if description == nil && isDeveloperAccessible == nil {
		return fmt.Sprintf(`
	resource "platform-orchestrator_resource_type" "test" {
		id = "%s"
		output_schema = "%s"
	}`, id, outputSchema)
	}

	if description == nil {
		return fmt.Sprintf(`
	resource "platform-orchestrator_resource_type" "test" {
		id = "%s"
		output_schema = "%s"
		is_developer_accessible = %t
	}`, id, outputSchema, *isDeveloperAccessible)
	}

	if isDeveloperAccessible == nil {
		return fmt.Sprintf(`
	resource "platform-orchestrator_resource_type" "test" {
		id = "%s"
		description = "%s"
		output_schema = <<EOT
	%s
	EOT
	}`, id, *description, outputSchema)
	}

	return fmt.Sprintf(`
	resource "platform-orchestrator_resource_type" "test" {
		id = "%s"
		description = "%s"
		output_schema = <<EOT
	%s
	EOT
		is_developer_accessible = %t
	}`, id, *description, outputSchema, *isDeveloperAccessible)
}
