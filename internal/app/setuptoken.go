package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// EnsureSetupToken makes first-run setup claimable only by someone who can read
// the server's log or environment. When NEBULACTRL_SETUP_TOKEN is not set and no
// user exists yet, it generates a one-time token, stores it in
// cfg.Installer.SetupToken (so POST /setup requires it, and the setup page asks
// for it) and logs it once. A restart before setup completes logs a new one.
// Call it before Build. It returns the token it generated, "" when it did not.
func EnsureSetupToken(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, logger *slog.Logger) (string, error) {
	if cfg.Installer.SetupToken != "" || pool == nil {
		return "", nil
	}
	n, err := db.New(pool).CountUsers(ctx)
	if err != nil {
		return "", fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		return "", nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate setup token: %w", err)
	}
	token := hex.EncodeToString(b)
	cfg.Installer.SetupToken = token
	if logger == nil {
		logger = slog.Default()
	}
	setupURL := "/setup?token=" + url.QueryEscape(token)
	if base := strings.TrimRight(cfg.PublicURL, "/"); base != "" {
		setupURL = base + setupURL
	}
	logger.Warn("first-run setup: no NEBULACTRL_SETUP_TOKEN is set, so this one-time token was generated; "+
		"open the setup URL or paste the token on the setup page to create the first account",
		"setup_token", token, "setup_url", setupURL)
	return token, nil
}
