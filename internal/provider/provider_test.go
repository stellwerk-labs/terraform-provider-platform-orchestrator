package provider

import (
	"cmp"
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// testAccProtoV6ProviderFactories is used to instantiate a provider during acceptance testing.
// The factory function is called for each Terraform CLI command to create a provider
// server that the CLI can connect to and interact with.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"platform-orchestrator": providerserver.NewProtocol6WithError(New("test")()),
}

func testAccPreCheck(t *testing.T) {
	checkEnvVar(t, PO_ORG_ID_ENV_VAR)
	checkEnvVar(t, PO_AUTH_TOKEN_ENV_VAR)
}

func checkEnvVar(t *testing.T, name string) {
	if v := os.Getenv(name); v == "" {
		t.Fatalf("Missing environment variable %s", name)
	}
}

// NewPlatformOrchestratorControlPlaneClient reaches the server under test as
// the provider does, with the PO_ environment variables, PO_CA_FILE included.
func NewPlatformOrchestratorControlPlaneClient(t *testing.T) *cp.ClientWithResponses {
	client, err := newAPIHTTPClient(os.Getenv(PO_CA_FILE_ENV_VAR))
	if err != nil {
		t.Fatalf("Error configuring the API TLS: %s", err)
	}
	cpc, err := cp.NewClientWithResponses(cmp.Or(os.Getenv(PO_API_URL_ENV_VAR), PO_DEFAULT_API_URL), cp.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+os.Getenv(PO_AUTH_TOKEN_ENV_VAR))
		return nil
	}), cp.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("Error creating Platform Orchestrator Controlplane client: %s", err)
	}
	return cpc
}

// The acceptance checks reach a server with a private certificate authority
// through PO_CA_FILE, exactly as the provider under test does.
func TestNewPlatformOrchestratorControlPlaneClientTrustsPOCAFile(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer a-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
	t.Setenv(PO_API_URL_ENV_VAR, server.URL)
	t.Setenv(PO_AUTH_TOKEN_ENV_VAR, "a-token")
	t.Setenv(PO_CA_FILE_ENV_VAR, caFile)

	response, err := NewPlatformOrchestratorControlPlaneClient(t).GetOrganizationWithResponse(t.Context(), "some-org")
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.StatusCode())
}

func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv(PO_API_URL_ENV_VAR, "")
	t.Setenv(PO_ORG_ID_ENV_VAR, "")
	t.Setenv(PO_AUTH_TOKEN_ENV_VAR, "")
}

func TestLoadClientConfig_basic(t *testing.T) {
	clearEnv(t)
	d := new(diag.Diagnostics)
	u, o, a := loadClientConfig(t.Context(), PlatformOrchestratorProviderModel{
		ApiUrl:    types.StringValue("https://some-api.com"),
		OrgId:     types.StringValue("some-org"),
		AuthToken: types.StringValue("some-token"),
	}, d)
	assert.Equal(t, "https://some-api.com", u)
	assert.Equal(t, "some-org", o)
	assert.Equal(t, "some-token", a)
	assert.Empty(t, d.Errors())
	assert.Empty(t, d.Warnings())
}

func TestLoadClientConfig_with_file(t *testing.T) {
	clearEnv(t)
	tf := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(tf, []byte(`{"default_org_id": "some-org", "token": "some-token"}`), 0600))

	d := new(diag.Diagnostics)
	u, o, a := loadClientConfig(t.Context(), PlatformOrchestratorProviderModel{
		ConfigFilePath: types.StringValue(tf),
		ApiUrl:         types.StringValue("https://some-api.com"),
	}, d)
	assert.Equal(t, "https://some-api.com", u)
	assert.Equal(t, "some-org", o)
	assert.Equal(t, "some-token", a)
	assert.Empty(t, d.Errors())
	assert.Empty(t, d.Warnings())
}

func TestLoadClientConfig_with_env(t *testing.T) {
	clearEnv(t)
	t.Setenv(PO_ORG_ID_ENV_VAR, "another-org")
	t.Setenv(PO_AUTH_TOKEN_ENV_VAR, "a-token")
	d := new(diag.Diagnostics)
	u, o, a := loadClientConfig(t.Context(), PlatformOrchestratorProviderModel{}, d)
	assert.Equal(t, "https://api.stellwerk.localhost", u)
	assert.Equal(t, "another-org", o)
	assert.Equal(t, "a-token", a)
	assert.Empty(t, d.Errors())
	assert.Empty(t, d.Warnings())
}

func TestLoadClientConfig_with_fallback_file(t *testing.T) {
	clearEnv(t)
	td := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", td)
	cd, _ := os.UserConfigDir()
	require.Contains(t, cd, td)
	require.NoError(t, os.MkdirAll(filepath.Join(cd, "octl"), 0700))
	tf := filepath.Join(cd, "octl", "config.yaml")
	require.NoError(t, os.WriteFile(tf, []byte(`{"default_org_id": "some-org", "token": "some-token"}`), 0600))
	d := new(diag.Diagnostics)

	ctx := tflogtest.RootLogger(t.Context(), os.Stdout)
	u, o, a := loadClientConfig(ctx, PlatformOrchestratorProviderModel{}, d)
	assert.Equal(t, "https://api.stellwerk.localhost", u)
	assert.Equal(t, "some-org", o)
	assert.Equal(t, "some-token", a)
	assert.Empty(t, d.Errors())
	assert.Empty(t, d.Warnings())
}
