package provider

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	dp "github.com/stellwerk-labs/terraform-provider-platform-orchestrator/internal/clients/platform-orchestrator-dp"
)

type deploymentAgentConfig struct {
	namespace, receiverSA, jobSA, gateway, image, caSecret, owner, kubeconfig, kubeContext, gatewayIP string
}

func deploymentAgentConfiguration(getenv func(string) string) (deploymentAgentConfig, error) {
	c := deploymentAgentConfig{
		namespace: getenv("MVM_ACC_AGENT_NAMESPACE"), receiverSA: getenv("MVM_ACC_AGENT_SERVICE_ACCOUNT"),
		jobSA: getenv("MVM_ACC_AGENT_JOB_SERVICE_ACCOUNT"), gateway: getenv("MVM_ACC_AGENT_GATEWAY_URL"),
		image: getenv("MVM_ACC_AGENT_IMAGE"), caSecret: getenv("MVM_ACC_AGENT_CA_SECRET"),
		owner: getenv("MVM_ACC_AGENT_OWNER"), kubeconfig: getenv("KUBECONFIG"),
		kubeContext: getenv("MVM_ACC_KUBE_CONTEXT"), gatewayIP: getenv("MVM_ACC_AGENT_GATEWAY_IP"),
	}
	name := regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)
	for _, value := range []string{c.namespace, c.receiverSA, c.jobSA, c.caSecret} {
		if !name.MatchString(value) || value == "default" {
			return c, errors.New("acceptance agent requires explicit non-default namespace, service accounts and CA Secret")
		}
	}
	u, err := url.Parse(c.gateway)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return c, errors.New("acceptance agent requires a normal HTTPS gateway URL on port 443")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$`).MatchString(c.image) {
		return c, errors.New("acceptance agent image must be digest-pinned")
	}
	if _, err := uuid.Parse(c.owner); err != nil || !filepath.IsAbs(c.kubeconfig) || c.kubeContext == "" || strings.ContainsAny(c.kubeContext, "\r\n") {
		return c, errors.New("acceptance agent requires an owner UUID, absolute kubeconfig and explicit context")
	}
	if c.gatewayIP != "" {
		if ip := net.ParseIP(c.gatewayIP); ip == nil || !ip.IsPrivate() {
			return c, errors.New("optional gateway host alias must be a private IP")
		}
	}
	return c, nil
}

type deploymentAgentObject struct {
	Metadata struct {
		Name, Namespace, UID string
		Labels               map[string]string
	} `json:"metadata"`
	Status struct {
		Conditions []struct{ Type, Status string } `json:"conditions"`
	} `json:"status"`
}

func deploymentAgentJobComplete(job *deploymentAgentObject) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Status == "True" && condition.Type == "Complete" {
			return true
		}
	}
	return false
}

type deploymentAgentFixture struct {
	config       deploymentAgentConfig
	namespaceUID string
	publicKey    string
	privateKey   string
}

func newDeploymentAgentFixture(t *testing.T) *deploymentAgentFixture {
	t.Helper()
	c, err := deploymentAgentConfiguration(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	f := &deploymentAgentFixture{config: c,
		publicKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
		privateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ns, err := f.get(ctx, "namespace", c.namespace)
	if err != nil || ns == nil || ns.Metadata.Name != c.namespace || ns.Metadata.UID == "" || ns.Metadata.Labels["stellwerk.dev/provider-acceptance"] != c.owner {
		t.Fatal("acceptance agent namespace is absent or has another owner")
	}
	f.namespaceUID = ns.Metadata.UID
	return f
}

func (f *deploymentAgentFixture) kubectl(ctx context.Context, input any, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args = append([]string{"--kubeconfig", f.config.kubeconfig, "--context", f.config.kubeContext, "--request-timeout=10s", "-n", f.config.namespace}, args...)
	// Only the normal kubectl client is executed, with explicit target and argv, never a shell or arbitrary hook.
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		cmd.Stdin = bytes.NewReader(data)
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("owned acceptance kubectl operation failed: %w", err)
	}
	return output, nil
}

func (f *deploymentAgentFixture) get(ctx context.Context, kind, name string) (*deploymentAgentObject, error) {
	data, err := f.kubectl(ctx, nil, "get", kind, name, "--ignore-not-found", "-o", "json")
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil, err
	}
	var obj deploymentAgentObject
	err = json.Unmarshal(data, &obj)
	return &obj, err
}

func (f *deploymentAgentFixture) ownNamespace(ctx context.Context) error {
	ns, err := f.get(ctx, "namespace", f.config.namespace)
	if err != nil {
		return err
	}
	if ns == nil || ns.Metadata.UID != f.namespaceUID || ns.Metadata.Labels["stellwerk.dev/provider-acceptance"] != f.config.owner {
		return errors.New("acceptance agent namespace ownership changed")
	}
	return nil
}

func (f *deploymentAgentFixture) create(ctx context.Context, kind, name string, body map[string]any) (*deploymentAgentObject, error) {
	if err := f.ownNamespace(ctx); err != nil {
		return nil, err
	}
	old, err := f.get(ctx, kind, name)
	if err != nil || old != nil {
		return nil, fmt.Errorf("acceptance agent object name is unavailable: %s/%s", kind, name)
	}
	body["apiVersion"], body["kind"] = "v1", kind
	body["metadata"] = map[string]any{"name": name, "namespace": f.config.namespace, "labels": map[string]string{"stellwerk.dev/provider-acceptance": f.config.owner}}
	data, err := f.kubectl(ctx, body, "create", "-f", "-", "-o", "json")
	if err != nil {
		return nil, err
	}
	var created deploymentAgentObject
	if err := json.Unmarshal(data, &created); err != nil {
		return nil, err
	}
	if created.Metadata.Name != name || created.Metadata.Namespace != f.config.namespace || created.Metadata.UID == "" || created.Metadata.Labels["stellwerk.dev/provider-acceptance"] != f.config.owner {
		return nil, errors.New("created acceptance object identity differs")
	}
	return &created, nil
}

func (f *deploymentAgentFixture) remove(parent context.Context, kind string, object *deploymentAgentObject) error {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	if err := f.ownNamespace(ctx); err != nil {
		return err
	}
	current, err := f.get(ctx, kind, object.Metadata.Name)
	if err != nil || current == nil {
		return err
	}
	if current.Metadata.UID != object.Metadata.UID || current.Metadata.Labels["stellwerk.dev/provider-acceptance"] != f.config.owner {
		return errors.New("refusing to delete a replacement acceptance object")
	}
	options := map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": object.Metadata.UID}}
	path := "/api/v1/namespaces/" + f.config.namespace + "/" + kind + "/" + object.Metadata.Name
	if _, err := f.kubectl(ctx, options, "delete", "--raw", path, "-f", "-"); err != nil {
		return err
	}
	for {
		current, err := f.get(ctx, kind, object.Metadata.Name)
		if err != nil || current == nil {
			return err
		}
		if current.Metadata.UID != object.Metadata.UID {
			return errors.New("acceptance object was replaced during deletion")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (f *deploymentAgentFixture) aliases() []map[string]any {
	if f.config.gatewayIP == "" {
		return nil
	}
	u, _ := url.Parse(f.config.gateway)
	return []map[string]any{{"ip": f.config.gatewayIP, "hostnames": []string{u.Hostname()}}}
}

func (f *deploymentAgentFixture) prepareCredential(t *testing.T) (*deploymentAgentObject, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name := "mvm-agent-" + uuid.NewString()[:12]
	t.Logf("creating owned receiver credential Secret %s", name)
	credential, err := f.create(ctx, "Secret", name, map[string]any{"type": "Opaque", "immutable": true, "stringData": map[string]string{"private-key.pem": f.privateKey}})
	if err == nil {
		t.Logf("created owned receiver credential Secret %s, UID %s", name, credential.Metadata.UID)
	}
	return credential, err
}

// drain reconnects only after the child has made its offline assertions. Real
// authenticated completion releases Runner ownership before the outer destroy.
func (f *deploymentAgentFixture) drain(t *testing.T, project, environment, runner string, credential *deploymentAgentObject) {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	ctx, cancelWork := context.WithTimeout(cleanupCtx, 90*time.Second)
	defer cancelWork()
	var receiver *deploymentAgentObject
	resolved := false
	defer func() {
		if !resolved {
			t.Logf("retaining owned credential Secret %s UID %s and any receiver for unresolved teardown", credential.Metadata.Name, credential.Metadata.UID)
			return
		}
		if receiver != nil {
			if err := f.remove(cleanupCtx, "pods", receiver); err != nil {
				t.Errorf("retaining acceptance receiver and credential after cleanup failure: %s", err)
				return
			}
		}
		if err := f.remove(cleanupCtx, "secrets", credential); err != nil {
			t.Errorf("retaining acceptance credential after cleanup failure: %s", err)
		}
	}()
	httpClient, err := newAPIHTTPClient(os.Getenv(PO_CA_FILE_ENV_VAR))
	if err != nil {
		t.Error(err)
		return
	}
	client, err := dp.NewClientWithResponses(cmp.Or(os.Getenv(PO_API_URL_ENV_VAR), PO_DEFAULT_API_URL), dp.WithHTTPClient(httpClient), dp.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+os.Getenv(PO_AUTH_TOKEN_ENV_VAR))
		return nil
	}))
	if err != nil {
		t.Error(err)
		return
	}
	response, err := client.ListDeploymentsWithResponse(ctx, os.Getenv(PO_ORG_ID_ENV_VAR), &dp.ListDeploymentsParams{ProjectId: &project, EnvId: &environment})
	if err != nil || response.JSON200 == nil || (response.JSON200.NextPageToken != nil && *response.JSON200.NextPageToken != "") || len(response.JSON200.Items) > 1 {
		t.Error("cannot identify the owned child Deployment for teardown")
		return
	}
	if len(response.JSON200.Items) == 0 {
		resolved = true
		return
	}
	deployment := response.JSON200.Items[0]
	detail, err := client.GetDeploymentWithResponse(ctx, os.Getenv(PO_ORG_ID_ENV_VAR), deployment.Id)
	if err != nil || detail.JSON200 == nil {
		t.Error("cannot read the owned child Deployment for teardown")
		return
	}
	if detail.JSON200.RunnerId != runner || detail.JSON200.ProjectId != project || detail.JSON200.EnvId != environment {
		t.Error("child Deployment scope differs")
		return
	}
	jobID := deployment.Id.String()
	if old, err := f.get(ctx, "job", jobID); err != nil || old != nil {
		t.Error("acceptance child Job unexpectedly exists before the receiver connects")
		return
	}
	name := credential.Metadata.Name
	env := []map[string]any{
		{"name": "ORG_ID", "value": os.Getenv(PO_ORG_ID_ENV_VAR)}, {"name": "RUNNER_ID", "value": runner},
		{"name": "PRIVATE_KEY", "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": name, "key": "private-key.pem"}}},
		{"name": "RUNNER_GATEWAY_URL", "value": f.config.gateway}, {"name": "RUNNER_GATEWAY_CA_FILE", "value": "/etc/runner-gateway/ca.crt"},
		{"name": "RUNNER_GATEWAY_JOB_CA_SECRET", "value": f.config.caSecret}, {"name": "RUNNER_GATEWAY_JOB_CA_KEY", "value": "ca.crt"},
	}
	spec := map[string]any{
		"restartPolicy": "Never", "serviceAccountName": f.config.receiverSA, "terminationGracePeriodSeconds": 5,
		"containers": []map[string]any{{"name": "receiver", "image": f.config.image, "imagePullPolicy": "IfNotPresent", "args": []string{"remote"}, "env": env,
			"resources":    map[string]any{"requests": map[string]string{"cpu": "50m", "memory": "64Mi"}, "limits": map[string]string{"cpu": "500m", "memory": "256Mi"}},
			"volumeMounts": []map[string]any{{"name": "gateway-ca", "mountPath": "/etc/runner-gateway", "readOnly": true}}}},
		"volumes": []map[string]any{{"name": "gateway-ca", "secret": map[string]any{"secretName": f.config.caSecret, "items": []map[string]string{{"key": "ca.crt", "path": "ca.crt"}}}}},
	}
	if aliases := f.aliases(); aliases != nil {
		spec["hostAliases"] = aliases
	}
	receiver, err = f.create(ctx, "Pod", name, map[string]any{"spec": spec})
	if err != nil {
		t.Error(err)
		return
	}
	t.Logf("created real receiver Pod %s, UID %s", name, receiver.Metadata.UID)
	var jobUID string
	for {
		job, err := f.get(ctx, "job", jobID)
		if err != nil {
			t.Error(err)
			return
		}
		if job != nil {
			if jobUID == "" {
				jobUID = job.Metadata.UID
			}
			if job.Metadata.UID != jobUID {
				t.Error("owned child Job was replaced")
				return
			}
			if deploymentAgentJobComplete(job) {
				current, err := client.GetDeploymentWithResponse(ctx, os.Getenv(PO_ORG_ID_ENV_VAR), deployment.Id)
				if err == nil && current.JSON200 != nil && current.JSON200.CompletedAt != nil && current.JSON200.Status == "succeeded" {
					resolved = true
					t.Logf("observed real receiver completed Job UID %s and succeeded Deployment %s", jobUID, jobID)
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Error("real acceptance agent did not complete the child Deployment before teardown deadline")
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}
