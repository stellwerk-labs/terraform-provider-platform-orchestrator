package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoreCandidateSpecRequiresExplicitMatchingSource(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mismatch bool
	}{
		{name: "missing source"}, {name: "different source", mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := t.TempDir()
			for _, path := range []string{
				"scripts/check-api-specs.sh",
				"internal/clients/platform-orchestrator-cp/SOURCE.md", "internal/clients/platform-orchestrator-cp/spec.yaml",
				"internal/clients/platform-orchestrator-dp/SOURCE.md", "internal/clients/platform-orchestrator-dp/spec.yaml",
				"internal/clients/pipelines/SOURCE.md", "internal/clients/pipelines/spec.yaml",
			} {
				data, err := os.ReadFile(filepath.Join("..", path))
				require.NoError(t, err)
				if path == "internal/clients/platform-orchestrator-cp/SOURCE.md" {
					data = regexp.MustCompile("(?m)^- Qualification: `[^`]+`$").ReplaceAll(data, []byte("- Qualification: `local-candidate`"))
					require.Contains(t, string(data), "- Qualification: `local-candidate`")
				}
				target := filepath.Join(fixture, path)
				require.NoError(t, os.MkdirAll(filepath.Dir(target), 0700))
				require.NoError(t, os.WriteFile(target, data, 0600))
			}
			t.Setenv("CP_SPEC_PATH", "")
			t.Setenv("DP_SPEC_PATH", filepath.Join(fixture, "internal", "clients", "platform-orchestrator-dp", "spec.yaml"))
			t.Setenv("PIPELINES_SPEC_PATH", filepath.Join(fixture, "internal", "clients", "pipelines", "spec.yaml"))
			if tc.mismatch {
				path := filepath.Join(t.TempDir(), "different.yaml")
				require.NoError(t, os.WriteFile(path, []byte("openapi: wrong\n"), 0600))
				t.Setenv("CP_SPEC_PATH", path)
			}
			command := exec.CommandContext(t.Context(), "bash", "scripts/check-api-specs.sh")
			command.Dir = fixture
			output, err := command.CombinedOutput()
			require.Error(t, err)
			if tc.mismatch {
				require.Contains(t, string(output), "internal/clients/platform-orchestrator-cp/spec.yaml differs")
			} else {
				require.Contains(t, string(output), "Unpublished Core candidate requires explicit CP_SPEC_PATH")
			}
		})
	}
}
