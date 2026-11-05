package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAccDeploymentAgentConfigurationLocal(t *testing.T) {
	valid := map[string]string{
		"MVM_ACC_AGENT_NAMESPACE":           "provider-acceptance-agent",
		"MVM_ACC_AGENT_SERVICE_ACCOUNT":     "command-receiver",
		"MVM_ACC_AGENT_JOB_SERVICE_ACCOUNT": "deployment-job",
		"MVM_ACC_AGENT_GATEWAY_URL":         "https://gateway.example.test/runner-gateway",
		"MVM_ACC_AGENT_IMAGE":               "example.test/runner:v3@sha256:" + strings.Repeat("a", 64),
		"MVM_ACC_AGENT_CA_SECRET":           "gateway-ca",
		"MVM_ACC_AGENT_OWNER":               "52ce954c-f32f-4aec-b72c-456ad6690ab3",
		"KUBECONFIG":                        "/tmp/owned-acceptance/kubeconfig",
		"MVM_ACC_KUBE_CONTEXT":              "owned-acceptance",
	}
	if _, err := deploymentAgentConfiguration(func(name string) string { return valid[name] }); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, key, value string }{
		{"shared-default-namespace", "MVM_ACC_AGENT_NAMESPACE", "default"},
		{"floating-image", "MVM_ACC_AGENT_IMAGE", "example.test/runner:latest"},
		{"plaintext-gateway", "MVM_ACC_AGENT_GATEWAY_URL", "http://gateway.example.test"},
		{"host-port-forward-gateway", "MVM_ACC_AGENT_GATEWAY_URL", "https://gateway.example.test:18443/runner-gateway"},
		{"credentials-in-url", "MVM_ACC_AGENT_GATEWAY_URL", "https://user:password@gateway.example.test"},
		{"public-host-alias", "MVM_ACC_AGENT_GATEWAY_IP", "203.0.113.1"},
		{"missing-owner", "MVM_ACC_AGENT_OWNER", ""},
		{"implicit-context", "MVM_ACC_KUBE_CONTEXT", ""},
		{"relative-kubeconfig", "KUBECONFIG", "kubeconfig"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := deploymentAgentConfiguration(func(name string) string {
				if name == test.key {
					return test.value
				}
				return valid[name]
			})
			if err == nil {
				t.Fatal("unsafe or ambiguous acceptance target was accepted")
			}
		})
	}
}

func TestAccDeploymentAgentJobCompletionLocal(t *testing.T) {
	for _, test := range []struct {
		name, status string
		terminal     bool
	}{
		{"completed", `{"conditions":[{"type":"Complete","status":"True"}]}`, true},
		{"failed-is-not-authenticated-success", `{"conditions":[{"type":"Failed","status":"True"}]}`, false},
		{"retrying-failed-pod", `{"active":1,"failed":1}`, false},
		{"condition-not-true", `{"conditions":[{"type":"Failed","status":"False"}]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var job deploymentAgentObject
			if err := json.Unmarshal([]byte(`{"status":`+test.status+`}`), &job); err != nil {
				t.Fatal(err)
			}
			if deploymentAgentJobComplete(&job) != test.terminal {
				t.Fatal("only a complete Job condition is accepted as positive completion")
			}
		})
	}
}
