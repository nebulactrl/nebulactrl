// Package config loads control-plane configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Config is the control-plane configuration.
type Config struct {
	DatabaseURL string
	// SecretKey is the current 32-byte key (NEBULACTRL_SECRET_KEY or
	// NEBULACTRL_SECRET_KEY_FILE); new values are sealed with it.
	SecretKey []byte
	// PreviousSecretKeys still open existing values until `rekey` has
	// re-encrypted them (NEBULACTRL_SECRET_KEY_PREVIOUS[_FILE], a list).
	PreviousSecretKeys [][]byte
	Listen             string
	// StatusListen is the address of the public status-page listener
	// (NEBULACTRL_STATUS_LISTEN, default ":8081"): a second HTTP server that
	// only renders published status pages by Host (internal/statuspublic).
	StatusListen string
	PublicURL    string
	// MCPEnabled with MCPFromEnv: NEBULACTRL_MCP_ENABLED was set, so it decides
	// the MCP endpoint and the web UI's switch is locked. Without it the
	// instance setting (the switch) decides.
	MCPEnabled       bool
	MCPFromEnv       bool
	LogLevel         slog.Level
	AgentBinariesDir string
	// CLIBinariesDir holds the CLI builds the control plane serves at
	// /download/cli (NEBULACTRL_CLI_BINARIES_DIR; internal/downloads documents the
	// layout). Empty in a development build: the routes answer 404.
	CLIBinariesDir string
	// BackupDir is the root of local control-plane backup destinations
	// (NEBULACTRL_BACKUP_DIR). Empty disables local destinations.
	BackupDir string
	// DumpMaxBytes is the largest dump file a database restore accepts
	// (NEBULACTRL_MAX_DUMP_UPLOAD_BYTES; default 5 GiB).
	DumpMaxBytes int64
	// Installer holds the settings only the installer sets (installer.go).
	Installer Installer
	// Update is the check for a newer release (update.go).
	Update Update
	// Instance holds the addresses and keys of the instance services (instance.go).
	Instance Instance
	// Timers holds the advanced timer overrides (timers.go); zero values keep
	// the built-in defaults.
	Timers Timers
}

// Load reads configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := &Config{
		DatabaseURL:      strings.TrimSpace(getenv("NEBULACTRL_DATABASE_URL")),
		Listen:           strings.TrimSpace(getenv("NEBULACTRL_LISTEN")),
		StatusListen:     strings.TrimSpace(getenv("NEBULACTRL_STATUS_LISTEN")),
		PublicURL:        strings.TrimRight(strings.TrimSpace(getenv("NEBULACTRL_PUBLIC_URL")), "/"),
		AgentBinariesDir: strings.TrimSpace(getenv("NEBULACTRL_AGENT_BINARIES_DIR")),
		CLIBinariesDir:   strings.TrimSpace(getenv("NEBULACTRL_CLI_BINARIES_DIR")),
		BackupDir:        strings.TrimSpace(getenv("NEBULACTRL_BACKUP_DIR")),
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.StatusListen == "" {
		c.StatusListen = ":8081"
	}
	var errs []error
	c.DumpMaxBytes = 5 << 30
	if v := strings.TrimSpace(getenv("NEBULACTRL_MAX_DUMP_UPLOAD_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err != nil || n < 1 {
			errs = append(errs, errors.New("NEBULACTRL_MAX_DUMP_UPLOAD_BYTES must be a positive number of bytes"))
		} else {
			c.DumpMaxBytes = n
		}
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("NEBULACTRL_DATABASE_URL is required"))
	}
	if key, err := loadSecretKey(getenv); err != nil {
		errs = append(errs, err)
	} else {
		c.SecretKey = key
	}
	if keys, err := loadPreviousKeys(getenv); err != nil {
		errs = append(errs, err)
	} else {
		c.PreviousSecretKeys = keys
	}
	if v := strings.TrimSpace(getenv("NEBULACTRL_MCP_ENABLED")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("NEBULACTRL_MCP_ENABLED: %w", err))
		}
		c.MCPEnabled, c.MCPFromEnv = b, true
	}
	if v := strings.TrimSpace(getenv("NEBULACTRL_LOG_LEVEL")); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("NEBULACTRL_LOG_LEVEL: %w", err))
		}
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, errors.New("NEBULACTRL_PUBLIC_URL must be an http(s) URL"))
		}
	}
	inst, err := loadInstaller(getenv)
	if err != nil {
		errs = append(errs, err)
	}
	c.Installer = inst
	upd, err := loadUpdate(getenv)
	if err != nil {
		errs = append(errs, err)
	}
	c.Update = upd
	instance, err := loadInstance(getenv)
	if err != nil {
		errs = append(errs, err)
	}
	c.Instance = instance
	timers, err := LoadTimers(getenv)
	if err != nil {
		errs = append(errs, err)
	}
	c.Timers = timers
	return c, errors.Join(errs...)
}

// SecureCookies reports whether session cookies carry the Secure attribute.
func (c *Config) SecureCookies() bool {
	return strings.HasPrefix(strings.ToLower(c.PublicURL), "https://")
}

// PublicOrigin returns scheme://host of PublicURL, or "" when unset.
func (c *Config) PublicOrigin() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// readFile reads a key file; a variable so tests can fake it.
var readFile = os.ReadFile

// secretValue returns the value of the variable name, or of the file named by
// name+"_FILE" (a Docker or systemd secret). Setting both is an error.
func secretValue(getenv func(string) string, name string) (value string, set bool, err error) {
	direct := strings.TrimSpace(getenv(name))
	path := strings.TrimSpace(getenv(name + "_FILE"))
	switch {
	case direct != "" && path != "":
		return "", false, fmt.Errorf("%s and %s_FILE are both set; set one", name, name)
	case path != "":
		raw, err := readFile(path)
		if err != nil {
			return "", false, fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(raw)), true, nil
	case direct != "":
		return direct, true, nil
	}
	return "", false, nil
}

func loadSecretKey(getenv func(string) string) ([]byte, error) {
	raw, set, err := secretValue(getenv, "NEBULACTRL_SECRET_KEY")
	if err != nil {
		return nil, err
	}
	if !set || raw == "" {
		return nil, errors.New("NEBULACTRL_SECRET_KEY (or NEBULACTRL_SECRET_KEY_FILE) is required (generate one with `nebulactrl-server keygen`)")
	}
	key, err := secretbox.ParseKey(raw)
	if err != nil {
		return nil, fmt.Errorf("NEBULACTRL_SECRET_KEY: %w", err)
	}
	return key, nil
}

func loadPreviousKeys(getenv func(string) string) ([][]byte, error) {
	raw, set, err := secretValue(getenv, "NEBULACTRL_SECRET_KEY_PREVIOUS")
	if err != nil || !set {
		return nil, err
	}
	keys, err := secretbox.ParseKeyList(raw)
	if err != nil {
		return nil, fmt.Errorf("NEBULACTRL_SECRET_KEY_PREVIOUS: %w", err)
	}
	return keys, nil
}
