#!/usr/bin/env bash
set -euo pipefail

readonly iam_revision=c837c2c9a4530a842083be3b6711fe4289b8cf3e
temp_dir="$(mktemp -d)"
readonly temp_dir
trap 'rm -rf -- "$temp_dir"' EXIT

check_spec() {
  local repository="$1"
  local revision="$2"
  local vendored_spec="$3"
  local local_spec="${4:-}"
  local repository_name="${repository##*/}"
  local downloaded_spec="$temp_dir/$repository_name.yaml"
  local normalized_vendored="$temp_dir/$repository_name-vendored.yaml"
  local normalized_downloaded="$temp_dir/$repository_name-downloaded.yaml"

  if [ -n "$local_spec" ]; then
    cp "$local_spec" "$downloaded_spec"
  else
    curl --fail --silent --show-error --location \
      "https://raw.githubusercontent.com/$repository/$revision/openapi/spec.yaml" \
      --output "$downloaded_spec"
  fi

  awk 'NF { while (blank > 0) { print ""; blank-- } print; next } { blank++ }' "$vendored_spec" >"$normalized_vendored"
  awk 'NF { while (blank > 0) { print ""; blank-- } print; next } { blank++ }' "$downloaded_spec" >"$normalized_downloaded"

  if ! cmp --silent "$normalized_vendored" "$normalized_downloaded"; then
    echo "$vendored_spec differs from $repository@$revision/openapi/spec.yaml" >&2
    return 1
  fi
}

# source_value FILE LABEL prints the value of the single "- LABEL: `value`" line.
source_value() {
  local value
  value="$(sed -n "s/^- $2: \`\([^\`]*\)\`\$/\1/p" "$1")"
  if [[ -z "$value" || "$value" == *$'\n'* ]]; then
    echo "$1 must record exactly one $2" >&2
    return 1
  fi
  printf '%s\n' "$value"
}

check_spec_sha() {
  local vendored_spec="$1"
  local expected_sha="$2"
  local actual_sha
  actual_sha="$(shasum -a 256 "$vendored_spec" | awk '{print $1}')"
  if [[ "$actual_sha" != "$expected_sha" ]]; then
    echo "$vendored_spec SHA-256 $actual_sha does not match recorded $expected_sha" >&2
    return 1
  fi
}

# The Core and Data Plane pins and digests live in their SOURCE.md files.
cp_revision="$(source_value internal/clients/platform-orchestrator-cp/SOURCE.md Commit)"
dp_revision="$(source_value internal/clients/platform-orchestrator-dp/SOURCE.md Commit)"
check_spec_sha internal/clients/platform-orchestrator-cp/spec.yaml "$(source_value internal/clients/platform-orchestrator-cp/SOURCE.md 'File SHA-256')"
check_spec_sha internal/clients/platform-orchestrator-dp/spec.yaml "$(source_value internal/clients/platform-orchestrator-dp/SOURCE.md 'File SHA-256')"

check_spec stellwerk-labs/platform-orchestrator-cp "$cp_revision" internal/clients/platform-orchestrator-cp/spec.yaml "${CP_SPEC_PATH:-}"
check_spec stellwerk-labs/platform-orchestrator-dp "$dp_revision" internal/clients/platform-orchestrator-dp/spec.yaml "${DP_SPEC_PATH:-}"
check_spec stellwerk-labs/platform-orchestrator-iam "$iam_revision" internal/clients/platform-orchestrator-iam/spec.yaml "${IAM_SPEC_PATH:-}"
