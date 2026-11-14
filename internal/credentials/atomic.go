package credentials

import (
	"os"
	"path/filepath"
)

// Same-directory replacement prevents interrupted writes from truncating credentials.
func atomicWriteCredentials(path string, data []byte) error {
	path, err := resolveCredentialsPath(path)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".auth-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func resolveCredentialsPath(path string) (string, error) {
	// Resolve directories before cleaning ".." components that may follow symlinks.
	parent, name := filepath.Split(path)
	if parent == "" {
		parent = "."
	}
	parent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	path = filepath.Join(parent, name)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil || !os.IsNotExist(err) {
		return resolved, err
	}

	// Initialization may follow a symlink whose credential file does not exist yet.
	target, err := os.Readlink(path)
	if os.IsNotExist(err) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = parent + string(filepath.Separator) + target
	}
	return resolveCredentialsPath(target)
}
