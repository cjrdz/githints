package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigHasOllamaDisabled(t *testing.T) {
	cfg := Default()
	if cfg.Ollama.Enabled {
		t.Fatal("ollama should be disabled by default")
	}
	if cfg.Ollama.Endpoint != defaultEndpoint {
		t.Errorf("endpoint = %q, want %q", cfg.Ollama.Endpoint, defaultEndpoint)
	}
	if cfg.Ollama.Model != defaultModel {
		t.Errorf("model = %q, want %q", cfg.Ollama.Model, defaultModel)
	}
	if cfg.Ollama.TimeoutMS != defaultTimeoutMS {
		t.Errorf("timeout_ms = %d, want %d", cfg.Ollama.TimeoutMS, defaultTimeoutMS)
	}
	if cfg.Ollama.MaxDiffBytes != defaultMaxDiffBytes {
		t.Errorf("max_diff_bytes = %d, want %d", cfg.Ollama.MaxDiffBytes, defaultMaxDiffBytes)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ollama.Enabled {
		t.Fatal("ollama should be disabled when config file is absent")
	}
}

func TestLoadJSONOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".githints"), 0o755)
	data := []byte(`{"ollama":{"enabled":true,"model":"custom-model","timeout_ms":5000}}`)
	if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Ollama.Enabled {
		t.Fatal("ollama should be enabled from config")
	}
	if cfg.Ollama.Model != "custom-model" {
		t.Errorf("model = %q, want custom-model", cfg.Ollama.Model)
	}
	if cfg.Ollama.TimeoutMS != 5000 {
		t.Errorf("timeout_ms = %d, want 5000", cfg.Ollama.TimeoutMS)
	}
	// Endpoint should keep its default because it was not in JSON.
	if cfg.Ollama.Endpoint != defaultEndpoint {
		t.Errorf("endpoint = %q, want %q", cfg.Ollama.Endpoint, defaultEndpoint)
	}
}

func TestEnvOverridesJSON(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".githints"), 0o755)
	data := []byte(`{"ollama":{"enabled":false}}`)
	if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv("GITHINTS_OLLAMA_ENABLED", "true")
	t.Setenv("GITHINTS_OLLAMA_MODEL", "env-model")
	t.Setenv("GITHINTS_OLLAMA_TIMEOUT_MS", "1500")
	t.Setenv("GITHINTS_OLLAMA_MAX_DIFF_BYTES", "2048")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Ollama.Enabled {
		t.Fatal("ollama should be enabled by env override")
	}
	if cfg.Ollama.Model != "env-model" {
		t.Errorf("model = %q, want env-model", cfg.Ollama.Model)
	}
	if cfg.Ollama.TimeoutMS != 1500 {
		t.Errorf("timeout_ms = %d, want 1500", cfg.Ollama.TimeoutMS)
	}
	if cfg.Ollama.MaxDiffBytes != 2048 {
		t.Errorf("max_diff_bytes = %d, want 2048", cfg.Ollama.MaxDiffBytes)
	}
}

func TestLoadRejectsNonLoopbackEndpoint(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".githints"), 0o755)
	data := []byte(`{"ollama":{"enabled":true,"endpoint":"http://192.168.1.1:11434"}}`)
	if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for non-loopback endpoint")
	}
}

func TestLoadAllowsLoopbackEndpoint(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".githints"), 0o755)
	data := []byte(`{"ollama":{"enabled":true,"endpoint":"http://127.0.0.1:11434"}}`)
	if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Ollama.Enabled {
		t.Fatal("ollama should be enabled")
	}
}

func TestLoadAllowsNonLoopbackWithOverride(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".githints"), 0o755)
	data := []byte(`{"ollama":{"enabled":true,"endpoint":"http://example.com:11434"}}`)
	if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv(allowNonLoopbackEnv, "1")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ollama.Endpoint != "http://example.com:11434" {
		t.Errorf("endpoint = %q, want example.com", cfg.Ollama.Endpoint)
	}
}

func TestLoadDisabledOllamaSkipsValidation(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".githints"), 0o755)
	data := []byte(`{"ollama":{"enabled":false,"endpoint":"http://evil.example.com:11434"}}`)
	if err := os.WriteFile(filepath.Join(dir, ".githints", "config.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Load(dir)
	if err != nil {
		t.Fatalf("disabled ollama should not validate endpoint: %v", err)
	}
}
