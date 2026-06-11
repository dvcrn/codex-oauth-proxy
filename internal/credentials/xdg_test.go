package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultCredsPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home directory: %v", err)
	}

	t.Run("with XDG_CONFIG_HOME set", func(t *testing.T) {
		original := os.Getenv("XDG_CONFIG_HOME")
		defer os.Setenv("XDG_CONFIG_HOME", original)

		testPath := "/tmp/test-config"
		os.Setenv("XDG_CONFIG_HOME", testPath)

		result := DefaultCredsPath()
		expected := filepath.Join(testPath, "codex-oauth-proxy", "auth.json")

		if result != expected {
			t.Errorf("Expected %s, got %s", expected, result)
		}
	})

	t.Run("without XDG_CONFIG_HOME set", func(t *testing.T) {
		original := os.Getenv("XDG_CONFIG_HOME")
		defer os.Setenv("XDG_CONFIG_HOME", original)

		os.Unsetenv("XDG_CONFIG_HOME")

		result := DefaultCredsPath()
		expected := filepath.Join(homeDir, ".config", "codex-oauth-proxy", "auth.json")

		if result != expected {
			t.Errorf("Expected %s, got %s", expected, result)
		}
	})
}

func TestLegacyCredsPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home directory: %v", err)
	}

	result := LegacyCredsPath()
	expected := filepath.Join(homeDir, ".codex", "auth.json")

	if result != expected {
		t.Errorf("Expected %s, got %s", expected, result)
	}
}

func TestEnsureParentDir(t *testing.T) {
	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "nested", "dir", "auth.json")

	err := EnsureParentDir(testPath)
	if err != nil {
		t.Fatalf("EnsureParentDir failed: %v", err)
	}

	parentDir := filepath.Dir(testPath)
	info, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("Parent directory was not created: %v", err)
	}

	if !info.IsDir() {
		t.Error("Expected parent to be a directory")
	}

	expectedPerm := os.FileMode(0700)
	if info.Mode().Perm() != expectedPerm {
		t.Errorf("Expected permissions %v, got %v", expectedPerm, info.Mode().Perm())
	}
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("file exists", func(t *testing.T) {
		existingFile := filepath.Join(tmpDir, "exists.json")
		if err := os.WriteFile(existingFile, []byte("{}"), 0600); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		if !FileExists(existingFile) {
			t.Error("Expected FileExists to return true for existing file")
		}
	})

	t.Run("file does not exist", func(t *testing.T) {
		nonExistentFile := filepath.Join(tmpDir, "does-not-exist.json")

		if FileExists(nonExistentFile) {
			t.Error("Expected FileExists to return false for non-existent file")
		}
	})
}
