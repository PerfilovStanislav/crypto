package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirFindsConfigsUpwards(t *testing.T) {
	t.Setenv("CONFIG_DIR", "")
	root := t.TempDir()
	configs := filepath.Join(root, "configs")
	nested := filepath.Join(root, "back", "src")
	for _, d := range []string{configs, nested} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(configs, "config.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	got, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	// На macOS TempDir лежит за симлинком /var -> /private/var.
	want, _ := filepath.EvalSymlinks(configs)
	if got, _ = filepath.EvalSymlinks(got); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestDirPrefersEnv(t *testing.T) {
	t.Setenv("CONFIG_DIR", "/some/dir")
	if got, err := Dir(); err != nil || got != "/some/dir" {
		t.Fatalf("Dir() = %q, %v", got, err)
	}
}

// Конфиги репозитория читаются из configs/. config.yaml не в git — без него тест пропускается.
func TestInitLoadsRepoConfigs(t *testing.T) {
	t.Setenv("CONFIG_DIR", "")
	if _, err := Dir(); err != nil {
		t.Skip(err)
	}
	cfg := Init()
	if cfg.Analyzer.Pair == "" || len(cfg.Analyzer.Indicators) == 0 {
		t.Fatalf("pair config not loaded: pair=%q, indicator groups=%d", cfg.Analyzer.Pair, len(cfg.Analyzer.Indicators))
	}
}
