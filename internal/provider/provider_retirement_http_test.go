package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/require"

	cp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-cp"
)

const retirementRequestedAt = "2026-11-12T01:02:03Z"

// Exercise the real generated HTTP client and framework state, not a client mock.
// This wire fixture does not qualify a running Control Plane's retirement proof.
type providerRetirementHTTPFixture struct {
	mu             sync.Mutex
	reads          []int
	deleteRequests int
}

func (f *providerRetirementHTTPFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/orgs/example/module-providers/aws/old-account" {
		http.Error(w, "unexpected path", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodDelete {
		f.deleteRequests++
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if r.Method != http.MethodGet || len(f.reads) == 0 {
		http.Error(w, "unexpected method", http.StatusBadRequest)
		return
	}
	status := f.reads[0]
	if len(f.reads) > 1 {
		f.reads = f.reads[1:]
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusOK {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"org_id": "example", "provider_type": "aws", "id": "old-account",
			"source": "hashicorp/aws", "version_constraint": ">= 6.0.0",
			"configuration": map[string]any{"region": "eu-west-1"},
			"created_at":    "2026-11-11T00:00:00Z", "deletion_requested_at": retirementRequestedAt,
		})
	} else {
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Provider not found"})
	}
}

func providerRetirementState(t *testing.T, r *ProviderResource, timeout string) tfsdk.State {
	t.Helper()
	var response resource.SchemaResponse
	r.Schema(t.Context(), resource.SchemaRequest{}, &response)
	state := tfsdk.State{Schema: response.Schema}
	data := ProviderResourceModel{
		Id: types.StringValue("old-account"), ProviderType: types.StringValue("aws"),
		Source: types.StringValue("hashicorp/aws"), VersionConstraint: types.StringValue(">= 6.0.0"),
		Configuration:  jsontypes.NewNormalizedValue(`{"region":"eu-west-1"}`),
		DeletionPolicy: types.StringValue("delete"),
		Timeouts: timeouts.Value{Object: types.ObjectValueMust(
			map[string]attr.Type{"delete": types.StringType},
			map[string]attr.Value{"delete": types.StringValue(timeout)},
		)},
	}
	require.False(t, state.Set(t.Context(), &data).HasError())
	return state
}

func TestProviderRetirementKeepsOwnershipUntilConfirmed(t *testing.T) {
	fixture := &providerRetirementHTTPFixture{reads: []int{http.StatusOK}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	client, err := cp.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	r := &ProviderResource{cpClient: client, orgId: "example"}
	state := providerRetirementState(t, r, "250ms")
	response := resource.DeleteResponse{State: state}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	r.Delete(ctx, resource.DeleteRequest{State: state}, &response)
	require.True(t, response.Diagnostics.HasError())
	require.Contains(t, response.Diagnostics.Errors()[0].Summary(), "Provider deletion is pending")
	require.True(t, response.State.Raw.Equal(state.Raw), "a timeout must preserve ownership and cleanup configuration")
	fixture.mu.Lock()
	requests := fixture.deleteRequests
	fixture.reads = []int{http.StatusNotFound}
	fixture.mu.Unlock()
	require.Equal(t, 1, requests)

	// The same retained state can retry the idempotent retirement request.
	response = resource.DeleteResponse{State: state}
	r.Delete(ctx, resource.DeleteRequest{State: state}, &response)
	require.False(t, response.Diagnostics.HasError(), "%v", response.Diagnostics)
	require.True(t, response.State.Raw.IsNull())
	fixture.mu.Lock()
	requests = fixture.deleteRequests
	fixture.mu.Unlock()
	require.Equal(t, 2, requests)

	// A pending response must be polled again, rather than treated as removal.
	fixture.mu.Lock()
	fixture.reads = []int{http.StatusOK, http.StatusNotFound}
	fixture.mu.Unlock()
	require.NoError(t, r.waitForProviderRemoval(ctx, "aws", "old-account"))
}

func TestProviderResourceAndDataSourceReadPendingRetirement(t *testing.T) {
	fixture := &providerRetirementHTTPFixture{reads: []int{http.StatusOK}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(server.Close)
	client, err := cp.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	r := &ProviderResource{cpClient: client, orgId: "example"}
	state := providerRetirementState(t, r, "250ms")
	response := resource.ReadResponse{State: state}
	r.Read(t.Context(), resource.ReadRequest{State: state}, &response)
	require.False(t, response.Diagnostics.HasError(), "%v", response.Diagnostics)
	var result ProviderResourceModel
	require.False(t, response.State.Get(t.Context(), &result).HasError())
	require.Equal(t, retirementRequestedAt, result.DeletionRequestedAt.ValueString())
	require.JSONEq(t, `{"region":"eu-west-1"}`, result.Configuration.ValueString())

	d := &ProviderDataSource{cpClient: client, orgId: "example"}
	var schemaResponse datasource.SchemaResponse
	d.Schema(t.Context(), datasource.SchemaRequest{}, &schemaResponse)
	input := tfsdk.State{Schema: schemaResponse.Schema}
	data := ProviderDataSourceModel{Id: result.Id, ProviderType: result.ProviderType}
	require.False(t, input.Set(t.Context(), &data).HasError())
	dataResponse := datasource.ReadResponse{State: input}
	d.Read(t.Context(), datasource.ReadRequest{Config: tfsdk.Config(input)}, &dataResponse)
	require.False(t, dataResponse.Diagnostics.HasError(), "%v", dataResponse.Diagnostics)
	require.False(t, dataResponse.State.Get(t.Context(), &data).HasError())
	require.Equal(t, retirementRequestedAt, data.DeletionRequestedAt.ValueString())
	require.JSONEq(t, `{"region":"eu-west-1"}`, data.Configuration.ValueString())
}
