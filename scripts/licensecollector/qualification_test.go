package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseQualifiesBeforeSigningAndAuthenticatesBothContracts(t *testing.T) {
	data, err := os.ReadFile("../../.goreleaser.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		SBOMs []struct {
			Artifacts, Cmd string
			Documents, Env []string
		} `yaml:"sboms"`
		Signs    []struct{ Cmd, Artifacts string }
		Checksum struct {
			IDs        []string
			ExtraFiles []struct{ Glob string } `yaml:"extra_files"`
		}
		Release struct {
			ExtraFiles []struct{ Glob string } `yaml:"extra_files"`
		}
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.SBOMs) != 1 || config.SBOMs[0].Artifacts != "any" ||
		config.SBOMs[0].Cmd != "qualify-release-content.sh" ||
		len(config.SBOMs[0].Documents) != 1 || config.SBOMs[0].Documents[0] != "*.spdx.json" {
		t.Fatal("every archive must pass the OSS SBOM qualification pipe before checksums/signing")
	}
	env := strings.Join(config.SBOMs[0].Env, "\n")
	for _, required := range []string{"QUALIFY_SOURCE_SHA={{ .FullCommit }}", "QUALIFY_VERSION={{ .Version }}",
		"QUALIFY_TAG={{ .Tag }}", "QUALIFY_REPOSITORY={{ .Env.GITHUB_REPOSITORY }}",
		"QUALIFY_WORKFLOW_REF={{ .Env.GITHUB_WORKFLOW_REF }}", "QUALIFY_WORKFLOW_SHA={{ .Env.GITHUB_WORKFLOW_SHA }}",
		"QUALIFY_RUN_ID={{ .Env.GITHUB_RUN_ID }}", "QUALIFY_RUN_ATTEMPT={{ .Env.GITHUB_RUN_ATTEMPT }}"} {
		if !strings.Contains(env, required) {
			t.Fatalf("qualification lost immutable producer input %s", required)
		}
	}
	if len(config.Signs) != 1 || config.Signs[0].Cmd != "./scripts/sign-qualified-artifact.py" || config.Signs[0].Artifacts != "checksum" {
		t.Fatal("release signing must recheck qualified bytes")
	}
	if len(config.Checksum.IDs) != 1 || config.Checksum.IDs[0] != "provider-archives" ||
		len(config.Checksum.ExtraFiles) != 1 || config.Checksum.ExtraFiles[0].Glob != "terraform-registry-manifest.json" {
		t.Fatal("Registry checksums must select provider archives and the manifest, not proof companions")
	}
	for _, glob := range []string{"dist/*.provenance.json", "dist/release-content.json", "dist/release-content.json.sig"} {
		shipped := false
		for _, file := range config.Release.ExtraFiles {
			shipped = shipped || file.Glob == glob
		}
		if !shipped {
			t.Fatalf("metadata %s must ship under the independently signed full receipt contract", glob)
		}
	}
}
