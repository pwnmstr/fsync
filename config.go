package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SyncMapping represents a directory to be synchronized.
type SyncMapping struct {
	ID        string `json:"id"`
	LocalPath string `json:"local_path"`
}

// AppConfig represents the main application configuration.
type AppConfig struct {
	DeviceName string        `json:"device_name"`
	Mappings   []SyncMapping `json:"mappings"`
}

// LoadConfig reads the configuration from a file or creates a default one if it doesn't exist.
func LoadConfig(path string) (*AppConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Create a default config
			hostname, _ := os.Hostname()
			if hostname == "" {
				hostname = "UnknownDevice"
			}
			cfg := &AppConfig{
				DeviceName: hostname,
				Mappings:   []SyncMapping{},
			}
			err = SaveConfig(path, cfg)
			return cfg, err
		}
		return nil, err
	}
	defer file.Close()

	cfg := &AppConfig{}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// SaveConfig writes the configuration to a file.
func SaveConfig(path string, cfg *AppConfig) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(cfg)
}
