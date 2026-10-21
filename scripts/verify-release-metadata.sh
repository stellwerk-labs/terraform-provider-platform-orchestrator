#!/usr/bin/env bash
set -euo pipefail

# Validate `gh release view --json tagName,isDraft,isPrerelease,assets,body` on stdin.
# This command only validates data; it cannot create or publish a release.
release_tag="${1:?usage: verify-release-metadata.sh RELEASE_TAG NOTES_FILE < release.json}"
notes_file="${2:?provide the reviewed release notes file}"
test -s "$notes_file"
version_without_metadata="${release_tag%%+*}"
expected_prerelease=false
if [[ "$version_without_metadata" == *-* ]]; then
  expected_prerelease=true
fi

if ! jq -e --arg tag "$release_tag" --argjson prerelease "$expected_prerelease" --rawfile notes "$notes_file" '
  ("terraform-provider-platform-orchestrator_" + ($tag | ltrimstr("v"))) as $prefix
  | ["darwin_amd64", "darwin_arm64", "freebsd_386", "freebsd_amd64",
     "freebsd_arm", "freebsd_arm64", "linux_386", "linux_amd64", "linux_arm",
     "linux_arm64", "windows_386", "windows_amd64", "windows_arm64"] as $targets
  | (["release-content.json", "release-content.json.sig", $prefix + "_manifest.json",
      $prefix + "_SHA256SUMS", $prefix + "_SHA256SUMS.sig"] +
      [$targets[] | ($prefix + "_" + . + ".zip") as $zip |
       $zip, ($zip + ".spdx.json"), ($zip + ".provenance.json")]) as $expected
  | .tagName == $tag
    and .isDraft == false
    and .isPrerelease == $prerelease
    and (.body | type) == "string"
    and (.body | sub("\\n+$"; "")) == ($notes | sub("\\n+$"; ""))
    and ([.assets[].name] | sort) == ($expected | sort)
' > /dev/null; then
  printf 'Release metadata or required signed artifacts do not match %s (prerelease=%s); check reviewed notes too.\n' \
    "$release_tag" "$expected_prerelease" >&2
  exit 1
fi
