package buildinfo

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDefaultVersionMatchesFrontendPackage(t *testing.T) {
	raw, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if Version != pkg.Version {
		t.Fatalf("backend %q differs from frontend %q", Version, pkg.Version)
	}
}

func TestBetaDocumentationIdentifiesVersionAndTestingOnlyUse(t *testing.T) {
	const zh = "本程序尚未经过充分、严格的测试，仅供学习与测试使用，请勿用于生产环境。使用前请自行评估风险并备份重要数据。"
	const en = "This program has not undergone sufficient, rigorous testing. It is for learning and testing only; do not use it in production. Assess the risks yourself and back up important data before use."
	for name, disclaimer := range map[string]string{
		"README.md": zh, "README.en.md": en,
		"docs/operations.md": zh, "docs/security-notes.md": zh,
	} {
		raw, err := os.ReadFile("../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), disclaimer) || !strings.Contains(string(raw), Version) {
			t.Errorf("%s must identify the application version and testing-only disclaimer", name)
		}
		if name != "docs/security-notes.md" && !strings.Contains(string(raw), "--channel beta --version v"+Version) {
			t.Errorf("%s must show explicit beta installation", name)
		}
	}
}
