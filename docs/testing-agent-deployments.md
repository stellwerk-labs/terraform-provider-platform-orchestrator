# Testing disconnected-agent deployments

`TestAccDeploymentResource` keeps its agent disconnected while it checks a
plan-only step, `wait_for = false`, and the `executing` status after apply and
refresh. A nested Terraform test owns only the Deployment resource. Its deferred
teardown then starts the published Runner in `remote` mode and waits for the
real Kubernetes Job to complete and the API result to succeed. The outer Terraform test
still creates and destroys the Project, Environment Type, Runner, Rule and
Environment. The same ordering runs when a child assertion fails.

Force deletion does not discard potentially live Runner ownership. Do not replace
the receiver with a fabricated result, an early outcome acknowledgement, a SQL
update, or a longer test timeout.

The configured-server suite needs `TF_ACC`, the normal `PO_API_URL`, `PO_ORG_ID`,
`PO_AUTH_TOKEN` and `PO_CA_FILE`, plus these explicit fixture settings:

| Variable | Purpose |
| --- | --- |
| `KUBECONFIG` | Absolute path to the intended cluster configuration. |
| `MVM_ACC_KUBE_CONTEXT` | Explicit cluster context. |
| `MVM_ACC_AGENT_NAMESPACE` | Dedicated, non-default acceptance namespace. |
| `MVM_ACC_AGENT_OWNER` | UUID matching its `stellwerk.dev/provider-acceptance` label. |
| `MVM_ACC_AGENT_SERVICE_ACCOUNT` | Receiver ServiceAccount with namespace-scoped Job get/create, Pod list and Event list access. |
| `MVM_ACC_AGENT_JOB_SERVICE_ACCOUNT` | Job ServiceAccount with the required namespace-scoped state Secret and Lease access. |
| `MVM_ACC_AGENT_GATEWAY_URL` | HTTPS Runner gateway URL reachable from the cluster. |
| `MVM_ACC_AGENT_IMAGE` | Published, digest-pinned Runner image with its normal entrypoint. |
| `MVM_ACC_AGENT_CA_SECRET` | Existing public CA Secret, key `ca.crt`, trusted by the receiver and created Jobs. |
| `MVM_ACC_AGENT_GATEWAY_IP` | Optional private IP host alias for that gateway hostname, on both receiver and Job. |

Prepare the namespace, ServiceAccounts, RBAC and public CA Secret before running
the case. The helper generates a fresh Ed25519 pair and places only its public key
in Terraform configuration. It persists a unique private credential Secret before
the child can accept a Deployment, then creates the receiver Pod during teardown.
It checks namespace ownership and deletes its own objects with UID preconditions
only after positive completion (or when no child Deployment was created). An
unresolved teardown retains its key and receiver for the fixture owner, and fails
the test. Credentials are sent to kubectl on stdin and
mounted or referenced by the receiver; they are not command-line arguments.
Completed Jobs and state remain in the dedicated namespace for inspection and
the fixture owner's later cleanup. Teardown reserves 30 seconds for object
cleanup inside one 120-second budget; its API and receiver work is bounded to
90 seconds. No failure extends those deadlines or becomes a passing assertion.

The pure configuration controls are part of the existing contained gate:

```sh
go test -run '^TestAcc.*Local$' ./internal/provider/
```

The real receiver case still requires the configured-server target. Passing the
contained gate does not qualify a cluster, image, native execution or teardown.
