package scripts_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestReleaseChannelConfiguration(t *testing.T) {
	var config struct {
		Release struct {
			Prerelease string `yaml:"prerelease"`
			MakeLatest string `yaml:"make_latest"`
			ExtraFiles []struct {
				Glob string `yaml:"glob"`
			} `yaml:"extra_files"`
		} `yaml:"release"`
		Checksum struct {
			Algorithm  string   `yaml:"algorithm"`
			IDs        []string `yaml:"ids"`
			ExtraFiles []struct {
				Glob         string `yaml:"glob"`
				NameTemplate string `yaml:"name_template"`
			} `yaml:"extra_files"`
		} `yaml:"checksum"`
		Archives []struct {
			ID string `yaml:"id"`
		} `yaml:"archives"`
		Changelog struct {
			Disable string `yaml:"disable"`
		} `yaml:"changelog"`
		Signs []struct {
			Cmd       string   `yaml:"cmd"`
			Artifacts string   `yaml:"artifacts"`
			Args      []string `yaml:"args"`
		} `yaml:"signs"`
	}
	data, err := os.ReadFile("../.goreleaser.yml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &config))
	require.Equal(t, "auto", config.Release.Prerelease)
	require.Equal(t, "sha256", config.Checksum.Algorithm)
	require.Equal(t, []string{"provider-archives"}, config.Checksum.IDs)
	require.Len(t, config.Archives, 1)
	require.Equal(t, "provider-archives", config.Archives[0].ID)
	require.Len(t, config.Checksum.ExtraFiles, 1)
	require.Equal(t, "terraform-registry-manifest.json", config.Checksum.ExtraFiles[0].Glob)
	require.Equal(t, "{{ .ProjectName }}_{{ .Version }}_manifest.json", config.Checksum.ExtraFiles[0].NameTemplate)
	var publishedExtras []string
	for _, file := range config.Release.ExtraFiles {
		publishedExtras = append(publishedExtras, file.Glob)
	}
	require.Equal(t, []string{"terraform-registry-manifest.json", "dist/*.provenance.json", "dist/release-content.json", "dist/release-content.json.sig"}, publishedExtras)
	require.Empty(t, config.Changelog.Disable, "disabling changelog also suppresses the reviewed --release-notes file")
	require.Len(t, config.Signs, 1)
	require.Equal(t, "checksum", config.Signs[0].Artifacts)
	require.Equal(t, "./scripts/sign-qualified-artifact.py", config.Signs[0].Cmd)
	require.Equal(t, []string{
		"gpg", "{{ .FullCommit }}", "{{ .Tag }}", "{{ .Version }}",
		"${artifact}", "${signature}", "{{ .Env.GPG_FINGERPRINT }}",
	}, config.Signs[0].Args)

	latest, err := template.New("make_latest").Option("missingkey=error").Parse(config.Release.MakeLatest)
	require.NoError(t, err)
	for _, tc := range []struct {
		tag, prerelease, expected string
	}{
		{"v2.0.0-rc.1", "rc.1", "false"},
		{"v2.0.0-beta.2+build.1", "beta.2", "false"},
		{"v2.0.0", "", "true"},
		{"v2.0.0+build-with-dashes", "", "true"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			var rendered bytes.Buffer
			require.NoError(t, latest.Execute(&rendered, map[string]string{
				"Tag": tc.tag, "Prerelease": tc.prerelease,
			}))
			require.Equal(t, tc.expected, rendered.String())
		})
	}
}

func TestReleaseWorkflowSafety(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Needs string `yaml:"needs"`
			Steps []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	data, err := os.ReadFile("../.github/workflows/release.yml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	job := workflow.Jobs["goreleaser"]
	require.Equal(t, "github.repository == 'stellwerk-labs/terraform-provider-platform-orchestrator'", job.If)
	require.Equal(t, "test", job.Needs)
	var hasRelease, hasVerification bool
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "goreleaser/goreleaser-action@") {
			hasRelease = true
			require.Equal(t, "v2.18.1", step.With["version"])
			require.Equal(t, "release --clean --release-notes=docs/releases/${{ github.ref_name }}.md", step.With["args"])
			require.Equal(t, "${{ steps.import_gpg.outputs.fingerprint }}", step.Env["GPG_FINGERPRINT"])
			require.Equal(t, "${{ secrets.GPG_PASSPHRASE }}", step.Env["GPG_PASSPHRASE"])
		}
		if step.Name == "Verify release artifacts" {
			hasVerification = true
			require.Contains(t, step.Run, "set -euo pipefail")
			require.Contains(t, step.Run, "--json tagName,isDraft,isPrerelease,assets,body")
			require.Contains(t, step.Run, `| bash scripts/verify-release-metadata.sh "$RELEASE_TAG" "docs/releases/$RELEASE_TAG.md"`)
		}
	}
	require.True(t, hasRelease)
	require.True(t, hasVerification)

	data, err = os.ReadFile("../.github/workflows/test.yml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	var hasConfigTest, hasConfigInstall, hasConfigCheck, hasRegistryTests, hasPipelineTest bool
	configInstallIndex, configCheckIndex, pipelineTestIndex := -1, -1, -1
	for index, step := range workflow.Jobs["build"].Steps {
		if strings.Contains(step.Run, "python3 scripts/registry_release_test.py") {
			hasRegistryTests = true
		}
		if step.Run == "python3 scripts/registry-release-pipeline_test.py" {
			hasPipelineTest = true
			pipelineTestIndex = index
		}
		if step.Run == "go test -v ./scripts" {
			hasConfigTest = true
		}
		if strings.HasPrefix(step.Uses, "goreleaser/goreleaser-action@") {
			hasConfigInstall = true
			configInstallIndex = index
			require.Equal(t, "goreleaser/goreleaser-action@f06c13b6b1a9625abc9e6e439d9c05a8f2190e94", step.Uses)
			require.Equal(t, "v2.18.1", step.With["version"])
			require.Equal(t, "true", step.With["install-only"])
			require.Empty(t, step.With["args"])
			require.Empty(t, step.Env, "configuration validation needs no publication credentials")
		}
		if step.Run == "goreleaser check" {
			hasConfigCheck = true
			configCheckIndex = index
		}
	}
	require.True(t, hasConfigTest)
	require.True(t, hasConfigInstall)
	require.True(t, hasConfigCheck)
	require.True(t, hasRegistryTests)
	require.True(t, hasPipelineTest)
	require.Less(t, configInstallIndex, configCheckIndex, "the pinned action must install GoReleaser before its configuration check")
	require.Less(t, configCheckIndex, pipelineTestIndex, "configuration validation must precede the real pipeline fixture")
}

func TestReleaseMetadataValidation(t *testing.T) {
	const reviewedNotes = "# Provider release\n\nReviewed breaking changes.\n"
	notesFile := filepath.Join(t.TempDir(), "notes.md")
	require.NoError(t, os.WriteFile(notesFile, []byte(reviewedNotes), 0o600))
	for _, tc := range []struct {
		name, tag, publishedTag, missing string
		prerelease, draft, wantError     bool
		assetCount                       int
		mutation                         string
		badNotes                         bool
		body                             string
	}{
		{name: "stable", tag: "v2.0.0"},
		{name: "candidate", tag: "v2.0.0-rc.1", prerelease: true},
		{name: "candidate build metadata", tag: "v2.0.0-rc.1+build.1", prerelease: true},
		{name: "stable build metadata dashes", tag: "v2.0.0+build-with-dashes"},
		{name: "candidate not marked prerelease", tag: "v2.0.0-rc.1", wantError: true},
		{name: "stable marked prerelease", tag: "v2.0.0", prerelease: true, wantError: true},
		{name: "draft", tag: "v2.0.0", draft: true, wantError: true},
		{name: "wrong tag", tag: "v2.0.0", publishedTag: "v1.1.0", wantError: true},
		{name: "missing checksum", tag: "v2.0.0", missing: "_SHA256SUMS", wantError: true},
		{name: "missing signature", tag: "v2.0.0", missing: "_SHA256SUMS.sig", wantError: true},
		{name: "missing manifest", tag: "v2.0.0", missing: "_manifest.json", wantError: true},
		{name: "missing receipt", tag: "v2.0.0", missing: "release-content.json", wantError: true},
		{name: "missing receipt signature", tag: "v2.0.0", missing: "release-content.json.sig", wantError: true},
		{name: "unknown replacement", tag: "v2.0.0", mutation: "unknown", wantError: true},
		{name: "duplicate asset", tag: "v2.0.0", mutation: "duplicate", wantError: true},
		{name: "missing proof companion", tag: "v2.0.0", mutation: "missing-proof", wantError: true},
		{name: "too few artifacts", tag: "v2.0.0", assetCount: 15, wantError: true},
		{name: "empty notes", tag: "v2.0.0", badNotes: true, wantError: true},
		{name: "wrong notes", tag: "v2.0.0", badNotes: true, body: "Automatically generated summary", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.publishedTag == "" {
				tc.publishedTag = tc.tag
			}
			if tc.assetCount == 0 {
				tc.assetCount = 44
			}
			if !tc.badNotes {
				tc.body = reviewedNotes
			}
			prefix := "terraform-provider-platform-orchestrator_" + strings.TrimPrefix(tc.publishedTag, "v")
			assets := make([]map[string]string, 0, tc.assetCount)
			for _, suffix := range []string{"_SHA256SUMS", "_SHA256SUMS.sig", "_manifest.json"} {
				if suffix != tc.missing {
					assets = append(assets, map[string]string{"name": prefix + suffix})
				}
			}
			for _, name := range []string{"release-content.json", "release-content.json.sig"} {
				if name != tc.missing {
					assets = append(assets, map[string]string{"name": name})
				}
			}
			for _, target := range []string{"darwin_amd64", "darwin_arm64", "freebsd_386", "freebsd_amd64", "freebsd_arm", "freebsd_arm64", "linux_386", "linux_amd64", "linux_arm", "linux_arm64", "windows_386", "windows_amd64", "windows_arm64"} {
				for _, suffix := range []string{"", ".spdx.json", ".provenance.json"} {
					assets = append(assets, map[string]string{"name": fmt.Sprintf("%s_%s.zip%s", prefix, target, suffix)})
				}
			}
			if tc.assetCount < len(assets) && tc.missing == "" {
				assets = assets[:tc.assetCount]
			}
			switch tc.mutation {
			case "unknown":
				assets[len(assets)-1] = map[string]string{"name": "unknown.provenance.json"}
			case "duplicate":
				assets[len(assets)-1] = assets[0]
			case "missing-proof":
				assets = assets[:len(assets)-1]
			}
			input, err := json.Marshal(map[string]any{
				"tagName": tc.publishedTag, "isDraft": tc.draft,
				"isPrerelease": tc.prerelease, "assets": assets, "body": tc.body,
			})
			require.NoError(t, err)
			command := exec.Command("bash", "verify-release-metadata.sh", tc.tag, notesFile)
			command.Stdin = bytes.NewReader(input)
			output, err := command.CombinedOutput()
			if tc.wantError {
				require.Error(t, err, "%s", output)
				require.Contains(t, string(output), "Release metadata or required signed artifacts do not match")
			} else {
				require.NoError(t, err, "%s", output)
			}
		})
	}
}
