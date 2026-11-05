package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const deploymentScaffold = `
resource "random_id" "r" {
  byte_length = 4
}

resource "platform-orchestrator_project" "project" {
  id           = "project-${random_id.r.hex}"
}

resource "platform-orchestrator_kubernetes_agent_runner" "runner" {
  id = "runner-${random_id.r.hex}"
  runner_configuration = {
    key = %q
    job = {
      namespace = %q
      service_account = %q
      pod_template = %q
    }
  }
  state_storage_configuration = {
    type = "kubernetes"
    kubernetes_configuration = {
      namespace = %q
    }
  }
}

resource "platform-orchestrator_runner_rule" "rrule" {
  runner_id   = platform-orchestrator_kubernetes_agent_runner.runner.id
  project_id = platform-orchestrator_project.project.id
}

resource "platform-orchestrator_environment_type" "env_type" {
  id           = "env-type-${random_id.r.hex}"
}

resource "platform-orchestrator_environment" "env" {
  id           = "env-${random_id.r.hex}"
  project_id   = platform-orchestrator_project.project.id
  env_type_id  = platform-orchestrator_environment_type.env_type.id
  depends_on   = [platform-orchestrator_runner_rule.rrule]

  force_delete = true
}

`

const disconnectedDeployment = `
resource "platform-orchestrator_deployment" "deployment" {
  project_id   = %q
  env_id  = %q
  mode = "deploy"
  wait_for = false
  manifest = jsonencode({
    workloads = {
      main = {
        variables = {
          ANIMAL = "cat"
        }
      }
    }
  })
}
`

func TestAccDeploymentResource(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC to run configured-server acceptance tests")
	}
	fixture := newDeploymentAgentFixture(t)
	podSpec := map[string]any{
		"containers": []map[string]any{{
			"name": "main",
			"resources": map[string]any{
				"requests": map[string]string{"cpu": "100m", "memory": "128Mi"},
				"limits":   map[string]string{"cpu": "500m", "memory": "512Mi"},
			},
		}},
	}
	if aliases := fixture.aliases(); aliases != nil {
		podSpec["hostAliases"] = aliases
	}
	podTemplate, err := json.Marshal(map[string]any{"spec": podSpec})
	if err != nil {
		t.Fatal(err)
	}
	scaffold := fmt.Sprintf(deploymentScaffold, fixture.publicKey, fixture.config.namespace, fixture.config.jobSA, string(podTemplate), fixture.config.namespace)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		ExternalProviders:        map[string]resource.ExternalProvider{"random": {Source: "hashicorp/random", VersionConstraint: "3.7.2"}},
		Steps: []resource.TestStep{{Config: scaffold, Check: func(state *terraform.State) error {
			project := state.RootModule().Resources["platform-orchestrator_project.project"].Primary.ID
			environment := state.RootModule().Resources["platform-orchestrator_environment.env"].Primary.ID
			runner := state.RootModule().Resources["platform-orchestrator_kubernetes_agent_runner.runner"].Primary.ID
			credential, err := fixture.prepareCredential(t)
			if err != nil {
				return err
			}
			passed := t.Run("disconnected-deployment", func(child *testing.T) {
				// The child owns only the forget-only Deployment resource. Its deferred
				// receiver drain runs before the outer Terraform scaffold is destroyed,
				// including when a child assertion calls Fatal or FailNow.
				defer fixture.drain(child, project, environment, runner, credential)
				resource.Test(child, resource.TestCase{
					ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
					Steps: []resource.TestStep{
						{Config: fmt.Sprintf(disconnectedDeployment, project, environment), PlanOnly: true, ExpectNonEmptyPlan: true},
						{Config: fmt.Sprintf(disconnectedDeployment, project, environment), Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr("platform-orchestrator_deployment.deployment", "status", "executing"),
							resource.TestCheckResourceAttr("platform-orchestrator_deployment.deployment", "wait_for", "false"),
							resource.TestCheckResourceAttrSet("platform-orchestrator_deployment.deployment", "id"),
						)},
						{RefreshState: true, Check: resource.TestCheckResourceAttr("platform-orchestrator_deployment.deployment", "status", "executing")},
					},
				})
			})
			if !passed {
				return errors.New("disconnected Deployment child failed; original failure retained")
			}
			return nil
		}}},
	})
}

func TestAccDeploymentResource_bad_request(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck: func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"platform-orchestrator": providerserver.NewProtocol6WithError(New("test")()),
		},
		Steps: []resource.TestStep{
			{
				Config: `

resource "platform-orchestrator_deployment" "deployment" {
  project_id   = "does-not-exist"
  env_id  =  "does-not-exist"
  mode = "deploy"
  manifest = jsonencode({
    workloads = {
      main = {}
    }
  })
}
`, ExpectError: regexp.MustCompile(`The deployment was refused \(HTTP-409\): environment not found\.`),
			},
		},
	})
}
