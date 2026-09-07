#!/usr/bin/env bash
set -euo pipefail

# This test owns only newly named fixtures in a disposable local Orchestrator.
# No public provider, registry upload, real Runner or infrastructure is required.
client="${1:?usage: bash scripts/test-module-management-clients.sh terraform|tofu /absolute/provider/binary [catalogue|pin]}"
provider_binary="${2:?provide the locally built provider executable}"
scenario="${3:-catalogue}"
case "$client" in terraform | tofu) ;; *) echo "Choose terraform or tofu" >&2; exit 1 ;; esac
case "$scenario" in
  catalogue) fixture="module-management-cli"; export TF_VAR_external_artifact=false ;;
  catalogue-external) fixture="module-management-cli"; export TF_VAR_external_artifact=true ;;
  pin)
    fixture="module-pin-cli"
    : "${TF_VAR_project_uuid:?provide the dedicated deployed test Project UUID}"
    : "${TF_VAR_environment_uuid:?provide the dedicated deployed test Environment UUID}"
    : "${TF_VAR_module_uuid:?provide the effective Module UUID}"
    : "${TF_VAR_version_uuid:?provide its exact effective Version UUID}"
    ;;
  *) echo "Choose catalogue, catalogue-external or pin" >&2; exit 1 ;;
esac
case "${PO_API_URL:-}" in
  http://127.0.0.1:* | http://localhost:*) ;;
  *) echo "This acceptance script requires a disposable loopback-only PO_API_URL" >&2; exit 1 ;;
esac
: "${PO_ORG_ID:?set a dedicated test organization}"
: "${PO_AUTH_TOKEN:?set the local test service token without printing it}"
test -x "$provider_binary"
command -v "$client" >/dev/null
command -v jq >/dev/null

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
provider_dir="$(cd "$(dirname "$provider_binary")" && pwd)"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/stellwerk-provider-${client}.XXXXXX")"
chmod 700 "$test_dir"
export TF_CLI_CONFIG_FILE="$test_dir/terraform.rc"
export TF_IN_AUTOMATION=1 TF_INPUT=0
TF_VAR_catalogue_id="provider-client-$(date +%s)-$$"
export TF_VAR_catalogue_id
export TF_VAR_catalogue_status=active
cp "$repo_dir/internal/provider/testdata/$fixture/main.tf" "$test_dir/main.tf"
cat >"$TF_CLI_CONFIG_FILE" <<EOF
provider_installation {
  dev_overrides {
    "registry.terraform.io/stellwerk-labs/platform-orchestrator" = "$provider_dir"
  }
  direct {}
}
EOF

report_failure() {
  local result=$?
  if [ "$result" -ne 0 ]; then
    echo "Acceptance failed. Protected state and logs retained at $test_dir" >&2
  fi
}
trap report_failure EXIT
cd "$test_dir"

run() {
  local phase="$1"
  local command="$2"
  shift 2
  if ! "$client" "$command" -no-color "$@" >>"$test_dir/commands.log" 2>&1; then
    echo "$client $phase failed; inspect $test_dir/commands.log" >&2
    return 1
  fi
  echo "PASS $client: $phase"
}

no_op_plan() {
  run "no-op plan" plan -detailed-exitcode
}

assert_state() {
  local expression="$1"
  "$client" show -json | jq -e "$expression" >/dev/null
}

if [ "$scenario" = "pin" ]; then
  # The caller supplies a reserved, actually deployed fixture. The test changes
  # only its own Pin and append-only notes, never its deployment or Module.
  export TF_VAR_note_enabled=false
  export TF_VAR_removal_reason="Complete client Pin acceptance"
  run "create exact effective-version Pin" apply -auto-approve
  pin_id="$("$client" show -json | jq -er '.values.root_module.resources[] | select(.address == "platform-orchestrator_module_version_pin.release") | .values.id')"
  activation_id="$("$client" show -json | jq -er '.values.root_module.resources[] | select(.address == "platform-orchestrator_module_version_pin.release") | .values.activation_event_id')"
  no_op_plan
  export TF_VAR_note_enabled=true
  run "append Pin note" apply -auto-approve
  run "observe unchanged protection" apply -refresh-only -auto-approve
  assert_state "any(.values.root_module.resources[]; .address == \"platform-orchestrator_module_version_pin.release\" and .values.id == \"$pin_id\" and .values.activation_event_id == \"$activation_id\" and .values.status == \"active\" and .values.version_uuid == \"$TF_VAR_version_uuid\")"
  assert_state "any(.values.root_module.resources[]; .type == \"platform-orchestrator_module_version_pin_note\" and .values.activation_event_id == \"$activation_id\")"
  export TF_VAR_removal_reason="Reviewed removal context"
  run "update only local removal reason" apply -auto-approve
  no_op_plan
  "$client" state rm platform-orchestrator_module_version_pin.release >>"$test_dir/commands.log" 2>&1
  run "import existing Pin and creation history" import platform-orchestrator_module_version_pin.release "$pin_id"
  run "restore local removal reason after import" apply -auto-approve
  no_op_plan
  run "audited Unpin and retained note" destroy -auto-approve
  export TF_VAR_note_enabled=false
  run "recreate same Pin intent" apply -auto-approve
  assert_state "any(.values.root_module.resources[]; .address == \"platform-orchestrator_module_version_pin.release\" and .values.id != \"$pin_id\" and .values.activation_event_id != \"$activation_id\" and .values.status == \"active\")"
  no_op_plan
  run "final audited Unpin" destroy -auto-approve
  echo "PASS $client: exact Pins, append-only notes, unchanged protection, import, local updates and recreate"
  echo "Protected local evidence: $test_dir"
  exit 0
fi

# dev_overrides intentionally bypass registry installation; init is unnecessary
# for this local backend and there are no other provider dependencies.
run "publish and atomically promote" apply -auto-approve
run "observe promoted lifecycle" apply -refresh-only -auto-approve
no_op_plan
assert_state 'any(.values.root_module.resources[]; .address == "platform-orchestrator_module_version.release" and .values.lifecycle_status == "default")'
assert_state 'any(.values.root_module.resources[]; .address == "platform-orchestrator_resource_type.release" and (.values.module_contract | fromjson | .required == ["output_schema"]))'
assert_state 'any(.values.root_module.resources[]; .address == "platform-orchestrator_module_version.release" and (.values.definition | fromjson | .output_schema.type == "object" and (has("artifact_digest") | not)))'
if [ "$scenario" = "catalogue-external" ]; then
  assert_state 'any(.values.root_module.resources[]; .address == "platform-orchestrator_module_version.release" and .values.verification_status == "unverified" and (.values.definition | fromjson | .module_source != "inline" and has("source_revision")))'
fi
version_id="$("$client" show -json | jq -er '.values.root_module.resources[] | select(.address == "platform-orchestrator_module_version.release") | .values.id')"

for state in archived active archived active; do
  export TF_VAR_catalogue_status="$state"
  run "catalogue $state" apply -auto-approve
  assert_state "any(.values.root_module.resources[]; .address == \"platform-orchestrator_module_catalogue_entry.release\" and .values.status == \"$state\")"
  no_op_plan
done

run "retained-history destroy" destroy -auto-approve
run "import retained Resource Type" import platform-orchestrator_resource_type.release "$TF_VAR_catalogue_id"
run "import retained Provider" import platform-orchestrator_provider.release "random.$TF_VAR_catalogue_id"
run "import retained Module" import platform-orchestrator_module_catalogue_entry.release "$TF_VAR_catalogue_id"
run "import retained Version" import platform-orchestrator_module_version.release "$TF_VAR_catalogue_id/1.0.0"
assert_state 'any(.values.root_module.resources[]; .address == "platform-orchestrator_module_version.release" and (.values.definition | fromjson | .output_schema.type == "object" and (has("artifact_digest") | not)))'
assert_state 'any(.values.root_module.resources[]; .address == "platform-orchestrator_module_catalogue_entry.release" and .values.status == "archived")'
assert_state "any(.values.root_module.resources[]; .address == \"platform-orchestrator_module_version.release\" and .values.id == \"$version_id\" and .values.lifecycle_status == \"default\")"
run "re-adopt and unarchive" apply -auto-approve
no_op_plan
run "final retained-history destroy" destroy -auto-approve
echo "PASS $client: lifecycle, repeated commands, immutable identity, retention, import and no-op plans"
echo "Protected local evidence: $test_dir"
