package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
	"github.com/stretchr/testify/require"
)

// This byte store tests the custody boundary, not Terraform's persistence.
// The real Terraform lifecycle is exercised in deployment_queue_local_test.go.
type deploymentPrivateBytes struct{ raw []byte }

func (value *deploymentPrivateBytes) GetKey(context.Context, string) ([]byte, diag.Diagnostics) {
	return value.raw, nil
}
func (value *deploymentPrivateBytes) SetKey(_ context.Context, _ string, raw []byte) diag.Diagnostics {
	value.raw = append([]byte(nil), raw...)
	return nil
}

// Existing wire regressions exercise preparation/submission, not Terraform private-state persistence.
func startDeploymentWireTest(t *testing.T, r *DeploymentResource, data *DeploymentResourceModel, diagnostics *diag.Diagnostics) *age.X25519Identity {
	t.Helper()
	r.scopeFingerprint, r.credentialFingerprint = deploymentAuthority("http://wire-test", r.orgId, http.Header{"Authorization": []string{"fixture"}})
	operation, err := r.prepareDeployment(context.Background(), data, deploymentCustody{Version: 1})
	if err != nil {
		diagnostics.AddError(PO_INPUT_ERR, err.Error())
		return nil
	}
	identity, _ := r.doDeployment(t.Context(), data, operation, diagnostics)
	return identity
}

func privateDeploymentFixture(t *testing.T) (*DeploymentResource, DeploymentResourceModel, *deploymentOperation) {
	t.Helper()
	r := &DeploymentResource{orgId: "private-project-org"}
	r.scopeFingerprint, r.credentialFingerprint = deploymentAuthority("https://private.test", r.orgId, http.Header{"Authorization": []string{"Bearer fixture-credential"}})
	data := DeploymentResourceModel{ProjectId: types.StringValue("owned-project"), EnvId: types.StringValue("owned-env"), Manifest: types.StringValue(`{"workloads":{}}`), Mode: types.StringValue("deploy"), WaitFor: types.BoolValue(false), PipelineVersionID: types.StringNull(), DraftTests: types.MapNull(types.StringType)}
	operation, err := r.prepareDeployment(t.Context(), &data, deploymentCustody{Version: 1})
	require.NoError(t, err)
	return r, data, operation
}
func TestDeploymentCustodyRoundTripAndCorruptionPrivacy(t *testing.T) {
	_, _, operation := privateDeploymentFixture(t)
	operation.AcceptedID = uuid.NewString()
	raw, err := json.Marshal(deploymentCustody{Version: 1, Current: operation})
	require.NoError(t, err)
	saved, err := decodeDeploymentCustody(raw)
	require.NoError(t, err)
	require.Equal(t, operation.RequestKey, saved.Current.RequestKey)
	require.Equal(t, operation.AcceptedID, saved.Current.AcceptedID)
	require.Equal(t, deploymentDigest(operation.OutputsIdentity), deploymentDigest(saved.Current.OutputsIdentity))
	for name, mutate := range map[string]func(*deploymentOperation){
		"identity": func(value *deploymentOperation) { value.OutputsIdentity = "AGE-SECRET-KEY-CANARY-DO-NOT-LOG" },
		"recipient": func(value *deploymentOperation) {
			recipient := "wrong-recipient"
			value.Request.Deployment.EncryptedOutputsRecipient = &recipient
		},
		"prepared request": func(value *deploymentOperation) { value.Request.Deployment.EnvId = "other-env" },
		"request key":      func(value *deploymentOperation) { value.RequestKey = "bad-request-key" },
		"accepted ID":      func(value *deploymentOperation) { value.AcceptedID = "invalid-id" },
	} {
		t.Run(name, func(t *testing.T) {
			altered := *operation
			mutate(&altered)
			candidate, err := json.Marshal(deploymentCustody{Version: 1, Current: &altered})
			require.NoError(t, err)
			_, err = decodeDeploymentCustody(candidate)
			require.Error(t, err)
			if strings.Contains(err.Error(), "CANARY") {
				t.Fatal("private canary leaked into diagnostic")
			}
		})
	}
	for _, candidate := range [][]byte{[]byte(`null`), []byte(`{}`), []byte(`{"current":null}`), []byte(`{"version":null}`), []byte(`{"version":2}`), []byte(`{"version":1,"future":true}`), []byte(`{"version":1} {}`), []byte("AGE-SECRET-KEY-CANARY"), bytes.Repeat([]byte("x"), deploymentPrivateLimit+1)} {
		_, err := decodeDeploymentCustody(candidate)
		require.Error(t, err)
		if strings.Contains(err.Error(), "CANARY") {
			t.Fatal("private canary leaked into diagnostic")
		}
	}
	legacy, err := decodeDeploymentCustody(nil)
	require.NoError(t, err)
	require.Nil(t, legacy.Current)
}
func TestPendingDeploymentReusesExactBodyAndAuthority(t *testing.T) {
	r, data, operation := privateDeploymentFixture(t)
	selected := uuid.NewString()
	data.PipelineVersionID = types.StringValue(selected)
	operation.Request.Deployment.Pipeline = &dp.DeploymentPipelineSelection{Id: selected, Version: 7}
	operation.Intent = deploymentPreparedIntent(r.orgId, operation.Request)
	operation.RequestHash = deploymentDigest(operation.Request)
	custody := deploymentCustody{Version: 1, Pending: operation}
	require.NoError(t, custody.validate())
	replay, err := r.prepareDeployment(t.Context(), &data, custody)
	require.NoError(t, err)
	require.Equal(t, operation.RequestKey, replay.RequestKey)
	require.Equal(t, operation.RequestHash, replay.RequestHash)
	require.Equal(t, deploymentDigest(operation.OutputsIdentity), deploymentDigest(replay.OutputsIdentity))
	// No pipeline client is configured: replay must use the retained numeric version/body.
	require.Equal(t, 7, replay.Request.Deployment.Pipeline.Version)
	changed := data
	changed.Manifest = types.StringValue(`{"shared":{}}`)
	_, err = r.prepareDeployment(t.Context(), &changed, custody)
	require.Error(t, err)
	r.credentialFingerprint = deploymentDigest("another-credential")
	_, err = r.prepareDeployment(t.Context(), &data, custody)
	require.Error(t, err)
	require.NoError(t, r.validateDeploymentCustody(custody, data), "credential changes do not prohibit ordinary read-only custody")
	r.scopeFingerprint = deploymentDigest("another-origin")
	require.Error(t, r.validateDeploymentCustody(custody, data))
}
func TestAcceptedCustodyBindsExactObjectAndFreshIntent(t *testing.T) {
	r, data, operation := privateDeploymentFixture(t)
	operation.AcceptedID = uuid.NewString()
	data.Id = types.StringValue(operation.AcceptedID)
	custody := deploymentCustody{Version: 1, Current: operation}
	require.NoError(t, r.validateDeploymentCustody(custody, data))
	replacement, err := r.prepareDeployment(t.Context(), &data, custody)
	require.NoError(t, err)
	require.NotEqual(t, operation.RequestKey, replacement.RequestKey)
	if deploymentDigest(operation.OutputsIdentity) == deploymentDigest(replacement.OutputsIdentity) {
		t.Fatal("new intent reused output identity")
	}
	data.Id = types.StringValue(uuid.NewString())
	require.Error(t, r.validateDeploymentCustody(custody, data))
}
func TestMissingPrivateStorageStopsBeforeDeploymentPost(t *testing.T) {
	r, data, _ := privateDeploymentFixture(t)
	var response resource.CreateResponse
	var diagnostics diag.Diagnostics
	r.deploy(t.Context(), time.Second, &data, nil, response.Private, deploymentCustody{Version: 1}, &tfsdk.State{}, &diagnostics)
	require.True(t, diagnostics.HasError())
	// The DP client is nil: any attempted mutation would panic instead of passing this test.
	require.True(t, data.Id.IsNull())
}

func TestPendingDeploymentReplayPreservesWireRequest(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(fmt.Sprintf("captured=%t", captured), func(t *testing.T) {
			r, data, operation := privateDeploymentFixture(t)
			path := "/orgs/" + r.orgId + "/deployments"
			var expected any = operation.Request.Deployment
			if captured {
				module, snapshot := uuid.NewString(), uuid.New()
				operation.Request.DraftTestSnapshots = map[string]uuid.UUID{module: snapshot}
				var diagnostics diag.Diagnostics
				data.DraftTests, diagnostics = types.MapValueFrom(t.Context(), types.StringType, map[string]string{module: snapshot.String()})
				require.False(t, diagnostics.HasError())
				path = "/orgs/" + r.orgId + "/module-test-deployments"
				expected = operation.Request
			}
			operation.Intent = deploymentPreparedIntent(r.orgId, operation.Request)
			operation.RequestHash = deploymentDigest(operation.Request)
			wire, err := json.Marshal(expected)
			require.NoError(t, err)
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				require.Equal(t, path, request.URL.Path)
				require.Equal(t, operation.RequestKey, request.Header.Get("Idempotency-Key"))
				body, readErr := io.ReadAll(request.Body)
				require.NoError(t, readErr)
				require.Equal(t, deploymentDigest(wire), deploymentDigest(body))
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(server.Close)
			r.dpClient, err = dp.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			for range 2 {
				replay, prepareErr := r.prepareDeployment(t.Context(), &data, deploymentCustody{Version: 1, Pending: operation})
				require.NoError(t, prepareErr)
				_, err = r.submitPreparedDeployment(t.Context(), replay)
				require.NoError(t, err)
			}
			require.Equal(t, int64(2), calls.Load())
		})
	}
}

func TestDeploymentRetryRefusalRetainsOriginalCustody(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(fmt.Sprintf("captured=%t", captured), func(t *testing.T) {
			r, data, _ := privateDeploymentFixture(t)
			if captured {
				var diagnostics diag.Diagnostics
				data.DraftTests, diagnostics = types.MapValueFrom(t.Context(), types.StringType, map[string]string{uuid.NewString(): uuid.NewString()})
				require.False(t, diagnostics.HasError())
			}
			acceptedID := uuid.New()
			var calls atomic.Int64
			var mu sync.Mutex
			var originalKey, originalBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				attempt := calls.Add(1)
				body, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				if attempt == 1 {
					originalKey, originalBody = request.Header.Get("Idempotency-Key"), deploymentDigest(body)
					// Model committed admission followed by a gateway's lost response.
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				require.Equal(t, originalKey, request.Header.Get("Idempotency-Key"))
				require.Equal(t, originalBody, deploymentDigest(body))
				if attempt == 2 {
					// Authorization can refuse before the retained-key lookup.
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				require.NoError(t, json.NewEncoder(w).Encode(dp.Deployment{Id: acceptedID, OrgId: r.orgId, ProjectId: data.ProjectId.ValueString(), EnvId: data.EnvId.ValueString(), CreatedAt: time.Now(), Status: "executing"}))
			}))
			t.Cleanup(server.Close)
			client, err := newAPIHTTPClient("")
			require.NoError(t, err)
			r.dpClient, err = dp.NewClientWithResponses(server.URL, dp.WithHTTPClient(client))
			require.NoError(t, err)
			store := &deploymentPrivateBytes{}
			var diagnostics diag.Diagnostics
			r.deploy(t.Context(), time.Second, &data, nil, store, deploymentCustody{Version: 1}, &tfsdk.State{}, &diagnostics)
			require.True(t, diagnostics.HasError())
			require.Equal(t, int64(1), calls.Load(), "the configured transport must not hide a second mutation attempt")
			original, err := decodeDeploymentCustody(store.raw)
			require.NoError(t, err)
			require.NotNil(t, original.Pending)
			diagnostics = nil
			r.deploy(t.Context(), time.Second, &data, nil, store, original, &tfsdk.State{}, &diagnostics)
			require.True(t, diagnostics.HasError())
			require.Equal(t, int64(2), calls.Load())
			retained, err := decodeDeploymentCustody(store.raw)
			require.NoError(t, err)
			require.Equal(t, original.Pending, retained.Pending, "later refusal cannot prove original non-admission")
			changed := data
			changed.Manifest = types.StringValue(`{"shared":{}}`)
			_, err = r.prepareDeployment(t.Context(), &changed, retained)
			require.Error(t, err)
			require.Equal(t, int64(2), calls.Load())
			operation, err := r.prepareDeployment(t.Context(), &data, retained)
			require.NoError(t, err)
			diagnostics = nil
			identity, refused := r.doDeployment(t.Context(), &data, operation, &diagnostics)
			require.False(t, diagnostics.HasError())
			require.NotNil(t, identity)
			require.False(t, refused)
			require.Equal(t, acceptedID.String(), operation.AcceptedID)
			require.Equal(t, int64(3), calls.Load())
		})
	}
}

func TestDeploymentPostDoesNotReplayAfterConnectionLoss(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(fmt.Sprintf("captured=%t", captured), func(t *testing.T) {
			r, _, operation := privateDeploymentFixture(t)
			if captured {
				operation.Request.DraftTestSnapshots = map[string]uuid.UUID{uuid.NewString(): uuid.New()}
			}
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodGet {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				_, err := io.Copy(io.Discard, request.Body)
				require.NoError(t, err)
				calls.Add(1)
				hijacker, ok := w.(http.Hijacker)
				require.True(t, ok)
				connection, _, err := hijacker.Hijack()
				require.NoError(t, err)
				require.NoError(t, connection.Close())
			}))
			t.Cleanup(server.Close)
			client, err := newAPIHTTPClient("")
			require.NoError(t, err)
			// A reused connection plus Idempotency-Key/GetBody normally permits
			// Go's transport to replay a POST after losing its response.
			primed, err := client.Get(server.URL)
			require.NoError(t, err)
			require.NoError(t, primed.Body.Close())
			r.dpClient, err = dp.NewClientWithResponses(server.URL, dp.WithHTTPClient(client))
			require.NoError(t, err)
			_, err = r.submitPreparedDeployment(t.Context(), operation)
			require.Error(t, err)
			require.Equal(t, int64(1), calls.Load(), "a fully received POST must not replay after response loss")
		})
	}
}
