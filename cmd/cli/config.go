package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	envConfig = "NEBULACTRL_CONFIG" // overrides the config file path
	envURL    = "NEBULACTRL_URL"    // overrides config.url
	envToken  = "NEBULACTRL_TOKEN"  // overrides config.token
)

// config is the content of ~/.config/nebulactrl/config.json.
type config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// Team is the slug of the team Token belongs to; Profiles holds the saved
	// token per team slug (a token belongs to exactly one team).
	Team     string            `json:"team,omitempty"`
	Profiles map[string]string `json:"profiles,omitempty"`
}

// configPath returns $NEBULACTRL_CONFIG or ~/.config/nebulactrl/config.json.
func configPath() (string, error) {
	if p := os.Getenv(envConfig); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate config: %w", err)
	}
	return filepath.Join(home, ".config", "nebulactrl", "config.json"), nil
}

// loadConfig reads the config file (a missing file is an empty config) and
// applies the NEBULACTRL_URL / NEBULACTRL_TOKEN overrides.
func loadConfig() (config, error) {
	var cfg config
	path, err := configPath()
	if err != nil {
		return cfg, err
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return cfg, fmt.Errorf("read %s: %w", path, err)
	default:
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if v := os.Getenv(envURL); v != "" {
		cfg.URL = v
	}
	if v := os.Getenv(envToken); v != "" {
		cfg.Token = v
	}
	cfg.URL = strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	cfg.Token = strings.TrimSpace(cfg.Token)
	return cfg, nil
}

// saveConfig writes the config with mode 0600 (directory 0700), replacing the
// file atomically so a crash never leaves a truncated token file.
func saveConfig(cfg config) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// apiBase turns the configured control-plane URL into the API base URL.
func apiBase(url string) string {
	url = strings.TrimRight(url, "/")
	if strings.HasSuffix(url, "/api/v1") {
		return url
	}
	return url + "/api/v1"
}
