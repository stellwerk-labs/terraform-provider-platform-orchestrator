package scripts_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
		} `yaml:"release"`
		Checksum struct {
			Algorithm string `yaml:"algorithm"`
		} `yaml:"checksum"`
		Signs []struct {
			Artifacts string   `yaml:"artifacts"`
			Args      []string `yaml:"args"`
		} `yaml:"signs"`
	}
	data, err := os.ReadFile("../.goreleaser.yml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &config))
	require.Equal(t, "auto", config.Release.Prerelease)
	require.Equal(t, "sha256", config.Checksum.Algorithm)
	require.Len(t, config.Signs, 1)
	require.Equal(t, "checksum", config.Signs[0].Artifacts)
	require.Equal(t, []string{
		"--batch", "--pinentry-mode", "loopback",
		"--passphrase", "{{ .Env.GPG_PASSPHRASE }}",
		"--local-user", "{{ .Env.GPG_FINGERPRINT }}",
		"--output", "${signature}", "--detach-sign", "${artifact}",
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
			require.Equal(t, "release --clean", step.With["args"])
			require.Equal(t, "${{ steps.import_gpg.outputs.fingerprint }}", step.Env["GPG_FINGERPRINT"])
			require.Equal(t, "${{ secrets.GPG_PASSPHRASE }}", step.Env["GPG_PASSPHRASE"])
		}
		if step.Name == "Verify release artifacts" {
			hasVerification = true
			require.Contains(t, step.Run, "set -euo pipefail")
			require.Contains(t, step.Run, "--json tagName,isDraft,isPrerelease,assets")
			require.Contains(t, step.Run, `| bash scripts/verify-release-metadata.sh "$RELEASE_TAG"`)
		}
	}
	require.True(t, hasRelease)
	require.True(t, hasVerification)

	data, err = os.ReadFile("../.github/workflows/test.yml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	var hasConfigTest, hasConfigCheck bool
	for _, step := range workflow.Jobs["build"].Steps {
		if step.Run == "go test -v ./scripts" {
			hasConfigTest = true
		}
		if strings.HasPrefix(step.Uses, "goreleaser/goreleaser-action@") {
			hasConfigCheck = true
			require.Equal(t, "v2.18.1", step.With["version"])
			require.Equal(t, "check", step.With["args"])
			require.Empty(t, step.Env, "configuration validation needs no publication credentials")
		}
	}
	require.True(t, hasConfigTest)
	require.True(t, hasConfigCheck)
}

func TestReleaseMetadataValidation(t *testing.T) {
	for _, tc := range []struct {
		name, tag, publishedTag, missing string
		prerelease, draft, wantError     bool
		assetCount                       int
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
		{name: "too few artifacts", tag: "v2.0.0", assetCount: 15, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.publishedTag == "" {
				tc.publishedTag = tc.tag
			}
			if tc.assetCount == 0 {
				tc.assetCount = 16
			}
			prefix := "terraform-provider-platform-orchestrator_" + strings.TrimPrefix(tc.publishedTag, "v")
			assets := make([]map[string]string, 0, tc.assetCount)
			for _, suffix := range []string{"_SHA256SUMS", "_SHA256SUMS.sig", "_manifest.json"} {
				if suffix != tc.missing {
					assets = append(assets, map[string]string{"name": prefix + suffix})
				}
			}
			for len(assets) < tc.assetCount {
				assets = append(assets, map[string]string{"name": fmt.Sprintf("%s_platform_%d.zip", prefix, len(assets))})
			}
			input, err := json.Marshal(map[string]any{
				"tagName": tc.publishedTag, "isDraft": tc.draft,
				"isPrerelease": tc.prerelease, "assets": assets,
			})
			require.NoError(t, err)
			command := exec.Command("bash", "verify-release-metadata.sh", tc.tag)
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
