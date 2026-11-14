package credentials

import (
	"os"
	"path/filepath"
)

// Rename within the same directory so an interrupted refresh cannot truncate auth.json.
func atomicWriteCredentials(path string, data []byte) error {
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
