package api

import (
	"path/filepath"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/pkg/errors"
)

// ConfigPath returns the location of the on-disk config file.
func ConfigPath() (string, error) {
	dir, err := homedir.Dir()
	if err != nil {
		return "", errors.Errorf("cannot resolve home directory: %v", err)
	}
	return filepath.Join(dir, ".orders", "config.json"), nil
}
