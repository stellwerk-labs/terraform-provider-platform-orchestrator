package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
	"github.com/stretchr/testify/require"
)

func TestDeploymentWaitRejectsUnprovenCompletion(t *testing.T) {
	for _, scenario := range []string{"missing body", "foreign ID", "foreign org", "foreign project", "foreign environment", "missing completion", "failed", "executing", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			r, data, _ := privateDeploymentFixture(t)
			id, now := uuid.New(), time.Now()
			data.Id = types.StringValue(id.String())
			completion := &dp.Deployment{Id: id, OrgId: r.orgId, ProjectId: data.ProjectId.ValueString(), EnvId: data.EnvId.ValueString(), Status: "succeeded", CompletedAt: &now}
			switch scenario {
			case "missing body":
				completion = nil
			case "foreign ID":
				completion.Id = uuid.New()
			case "foreign org":
				completion.OrgId = "other-org"
			case "foreign project":
				completion.ProjectId = "other-project"
			case "foreign environment":
				completion.EnvId = "other-env"
			case "missing completion":
				completion.CompletedAt = nil
			default:
				completion.Status = scenario
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				require.Equal(t, "/orgs/"+r.orgId+"/deployments/"+id.String()+"/actions/wait-for-complete", request.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(completion))
			}))
			t.Cleanup(server.Close)
			var err error
			r.dpClient, err = dp.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			var diagnostics diag.Diagnostics
			r.waitForDeployment(t.Context(), &data, &diagnostics, nil)
			require.True(t, diagnostics.HasError(), diagnostics)
			require.Zero(t, diagnostics.WarningsCount())
			require.True(t, data.Outputs.IsNull())
		})
	}
}

func TestSucceededDeploymentOutputFaultWarnsButRefreshRemainsError(t *testing.T) {
	for _, fault := range []string{"HTTP refusal", "invalid ciphertext", "truncated stream"} {
		t.Run(fault, func(t *testing.T) {
			r, data, operation := privateDeploymentFixture(t)
			id, now := uuid.New(), time.Now()
			operation.AcceptedID = id.String()
			data.Id = types.StringValue(id.String())
			identity, err := operation.identity()
			require.NoError(t, err)
			var encrypted bytes.Buffer
			writer, err := age.Encrypt(&encrypted, identity.Recipient())
			require.NoError(t, err)
			_, err = writer.Write([]byte(strings.Repeat("sensitive-output-canary", 100)))
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			ciphertext := base64.StdEncoding.EncodeToString(encrypted.Bytes()[:encrypted.Len()-5])
			if fault == "invalid ciphertext" {
				ciphertext = "invalid-ciphertext-canary"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(request.URL.Path, "/actions/wait-for-complete"):
					require.NoError(t, json.NewEncoder(w).Encode(dp.Deployment{Id: id, OrgId: r.orgId, ProjectId: data.ProjectId.ValueString(), EnvId: data.EnvId.ValueString(), Status: "succeeded", CompletedAt: &now}))
				case strings.HasSuffix(request.URL.Path, "/encrypted-outputs"):
					if fault == "HTTP refusal" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(dp.DeploymentEncryptedOutputs{Raw: ciphertext}))
				default:
					t.Errorf("unexpected request %s", request.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			r.dpClient, err = dp.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			var diagnostics diag.Diagnostics
			r.waitForDeployment(t.Context(), &data, &diagnostics, identity)
			require.False(t, diagnostics.HasError(), diagnostics)
			require.Equal(t, 1, diagnostics.WarningsCount())
			require.Equal(t, "succeeded", data.Status.ValueString())
			require.False(t, data.CompletedAt.IsNull())
			require.True(t, data.Outputs.IsNull())
			require.NotContains(t, diagnostics[0].Detail(), "canary")
			diagnostics = nil
			r.recoverDeploymentOutputs(t.Context(), operation, &data, &diagnostics)
			require.True(t, diagnostics.HasError(), "ordinary refresh must still report the output fault")
			require.Zero(t, diagnostics.WarningsCount())
		})
	}
}
