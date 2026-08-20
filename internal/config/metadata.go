package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type InstallMetadata struct {
	InstallationID      string `json:"installation_id"`
	InstallDir          string `json:"install_dir"`
	ConfigDir           string `json:"config_dir"`
	DataDir             string `json:"data_dir"`
	LogDir              string `json:"log_dir"`
	DistributionID      string `json:"distribution_id"`
	DistributionVersion string `json:"distribution_version"`
	InitSystem          string `json:"init_system"`
	Architecture        string `json:"architecture"`
}

func LoadInstallMetadata(path string) (InstallMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return InstallMetadata{}, err
	}
	defer file.Close()
	var metadata InstallMetadata
	decoder := json.NewDecoder(io.LimitReader(file, 256<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return InstallMetadata{}, fmt.Errorf("decode install metadata: %w", err)
	}
	return metadata, nil
}

func SaveInstallMetadata(path string, metadata InstallMetadata) error {
	if strings.TrimSpace(metadata.InstallationID) == "" {
		return errors.New("installation_id is required")
	}
	paths := Paths{
		InstallDir: metadata.InstallDir,
		ConfigDir:  metadata.ConfigDir,
		DataDir:    metadata.DataDir,
		LogDir:     metadata.LogDir,
	}
	if err := paths.Validate(); err != nil {
		return err
	}
	return writeJSONAtomic(path, metadata, 0o644)
}
