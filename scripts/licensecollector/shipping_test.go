package main

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseArchivesKeepCompleteNoticeTree(t *testing.T) {
	const owner = "root"
	var config struct {
		Before struct{ Hooks []string }
		Builds []struct {
			Flags        []string
			ModTimestamp string `yaml:"mod_timestamp"`
		}
		Archives []struct {
			BuildsInfo struct{ Mtime, Owner, Group string } `yaml:"builds_info"`
			Files      []struct {
				Src  string
				Info struct {
					Mtime, Owner, Group string
					Mode                int
				}
			}
		}
	}
	data, err := os.ReadFile("../../.goreleaser.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hook := range config.Before.Hooks {
		found = found || hook == "./scripts/collect-licenses.sh --replace-generated licenses"
	}
	if !found {
		t.Fatal("release hook no longer generates the complete notice union")
	}
	if len(config.Builds) == 0 || len(config.Archives) == 0 {
		t.Fatal("release build/archive configuration is missing")
	}
	for _, build := range config.Builds {
		trimpath := false
		for _, flag := range build.Flags {
			trimpath = trimpath || flag == "-trimpath"
		}
		if !trimpath || build.ModTimestamp != "{{ .CommitTimestamp }}" {
			t.Fatal("release binary path/timestamp normalization is missing")
		}
	}
	for _, archive := range config.Archives {
		if archive.BuildsInfo.Mtime != "{{ .CommitDate }}" || archive.BuildsInfo.Owner != owner || archive.BuildsInfo.Group != owner {
			t.Fatal("archive binary metadata is not normalized")
		}
		required := map[string]bool{"LICENSE": false, "README.md": false, "licenses/**/*": false, "licenses/modules.json": false, "licenses/notices.sha256.json": false}
		for _, file := range archive.Files {
			if _, exists := required[file.Src]; !exists {
				continue
			}
			required[file.Src] = true
			if file.Info.Mtime != "{{ .CommitDate }}" || file.Info.Owner != owner || file.Info.Group != owner || file.Info.Mode != 0o644 {
				t.Fatal("archive notice metadata is not normalized")
			}
		}
		for name, found := range required {
			if !found {
				t.Fatalf("release archive omits %s", name)
			}
		}
	}
}
