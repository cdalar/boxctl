// Package config reads and writes boxctl's local config file
// (~/.boxctl/config.json), which holds the personal API token from
// `boxctl login` and (optionally) a non-default API URL.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DefaultAPIURL is boxctl-vms's public endpoint -- see its README's
// "Personal API tokens (the `boxctl` CLI)" for what this talks to.
const DefaultAPIURL = "https://vms-backend.boxctl.io"

type Config struct {
	APIURL string `json:"api_url"`
	Token  string `json:"token"`
}

func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".boxctl"), nil
}

func path() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// Load reads the config file, returning a zero-value Config with
// DefaultAPIURL (not an error) if it doesn't exist yet -- that's the
// normal state before the first `boxctl login`.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{APIURL: DefaultAPIURL}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	return &cfg, nil
}

// Save writes cfg to the config file, creating ~/.boxctl if needed.
// Mode 0600 -- the file holds a bearer token.
func Save(cfg *Config) error {
	d, err := dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	p, err := path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// Clear removes the config file (a no-op, not an error, if it's already
// gone) -- used by `boxctl logout`.
func Clear() error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
