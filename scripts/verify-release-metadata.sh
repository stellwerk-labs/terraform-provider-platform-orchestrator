#!/usr/bin/env bash
set -euo pipefail

# Validate `gh release view --json tagName,isDraft,isPrerelease,assets` on stdin.
# This command only validates data; it cannot create or publish a release.
release_tag="${1:?usage: verify-release-metadata.sh RELEASE_TAG < release.json}"
version_without_metadata="${release_tag%%+*}"
expected_prerelease=false
if [[ "$version_without_metadata" == *-* ]]; then
  expected_prerelease=true
fi

if ! jq -e --arg tag "$release_tag" --argjson prerelease "$expected_prerelease" '
  ("terraform-provider-platform-orchestrator_" + ($tag | ltrimstr("v"))) as $prefix
  | .tagName == $tag
    and .isDraft == false
    and .isPrerelease == $prerelease
    and (.assets | length) >= 16
    and ([.assets[].name] | index($prefix + "_SHA256SUMS") != null)
    and ([.assets[].name] | index($prefix + "_SHA256SUMS.sig") != null)
    and ([.assets[].name] | index($prefix + "_manifest.json") != null)
' > /dev/null; then
  printf 'Release metadata or required signed artifacts do not match %s (prerelease=%s).\n' \
    "$release_tag" "$expected_prerelease" >&2
  exit 1
fi
