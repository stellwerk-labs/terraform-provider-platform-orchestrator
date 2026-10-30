# Live Draft execution regression

`TestAccModuleDraftTestLiveLifecycle` uses real Orchestrator APIs, IAM and Runner
execution through the provider protocol. It does not emulate their business
behavior. Run it against a disposable, isolated installation with matching CP
and DP APIs. Never point this fixture at a customer Environment.

Prepare a dedicated Resource Type whose version-1 output schema is
`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`,
an unused Module for that type, and an Environment with a working Runner. Its
Module Rule must name both Project and Environment. The Module must have no
published Versions or open Draft. The test uses inline source and no Provider.

Supply credentials through the existing `PO_API_URL`, `PO_ORG_ID` and
`PO_AUTH_TOKEN` environment variables (plus `PO_CA_FILE` for a private CA).
Do not place tokens in HCL, shell history or committed fixture files.

```sh
export MVM_ACC_MODULE_ID=dedicated-module
export MVM_ACC_RESOURCE_TYPE=dedicated-type
export MVM_ACC_PROJECT_ID=dedicated-project
export MVM_ACC_ENV_ID=dedicated-environment
TF_ACC=1 TF_ACC_TERRAFORM_PATH="$(command -v terraform)" \
  go test ./internal/provider -run '^TestAccModuleDraftTestLiveLifecycle$' \
  -count=1 -timeout=180s
```

Repeat with `TF_ACC_TERRAFORM_PATH="$(command -v tofu)"`. The test declares the
full provider source and uses the same protocol implementation for both tools.
The explicit live-fixture variables keep it out of plan-only test runs.

The test executes A under intended `1.0.0`, saves B without changing the Draft
UUID, and verifies that the capture, deployment UUID and output still describe
A. Selecting reviewed revision 2 must create another capture and deployment
whose decrypted output is B. The first capture remains readable, published
Version history remains empty, and each step must settle to an empty plan.
This detects incorrect computed revision/identity plan modifiers and an Update
handler that ignores changes to the selected test snapshots.

Terraform teardown discards the open Draft and forgets capture/deployment state.
The fixture owner must destroy the Environment's actual infrastructure and
remove its scoped Rules. Immutable authoring and capture history are retained by
design; use an isolated database or namespace for eventual complete cleanup.
