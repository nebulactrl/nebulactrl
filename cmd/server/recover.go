package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"strings"
	"syscall"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// operatorLabel is the audit actor label of the recovery commands. The actor
// type is "system": nobody is signed in when an operator runs them.
const operatorLabel = "nebulactrl-server (operator command)"

// recoverEnv is what the recovery commands need: a database, the account
// service, the session store (to end the account's sessions), and the terminal.
type recoverEnv struct {
	pool     *pgxpool.Pool
	svc      *auth.Service
	sessions *scs.SessionManager
	in       io.Reader
	out      io.Writer
}

// openRecoverEnv reads the same configuration the server does and connects to
// the database. It does not migrate and it does not start any background work.
func openRecoverEnv(ctx context.Context, cmd *cobra.Command) (*recoverEnv, func(), error) {
	_ = hardenProcess()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return nil, nil, fmt.Errorf("configuration: %w", err)
	}
	// The recovery commands never open a sealed value.
	secretbox.Wipe(cfg.SecretKey)
	for _, k := range cfg.PreviousSecretKeys {
		secretbox.Wipe(k)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	pool, err := connect(ctx, cfg.DatabaseURL, logger)
	if err != nil {
		return nil, nil, err
	}
	svc := auth.NewService(pool, nil)
	svc.Configure(nil, cfg.PublicURL)
	sm := scs.New()
	sm.Store = pgxstore.NewWithCleanupInterval(pool, 0)
	return &recoverEnv{pool: pool, svc: svc, sessions: sm, in: cmd.InOrStdin(), out: cmd.OutOrStdout()}, pool.Close, nil
}

// confirmEmail asks the operator to type the account email, or accepts it from
// --confirm. Anything else changes nothing.
func (e *recoverEnv) confirmEmail(acct *auth.OperatorAccount, given string) error {
	typed := given
	if typed == "" {
		fmt.Fprintf(e.out, "Type the account email (%s) to confirm: ", acct.Email)
		line, err := bufio.NewReader(e.in).ReadString('\n')
		if err != nil && line == "" {
			return errors.New("no confirmation typed; nothing was changed")
		}
		typed = line
	}
	if !strings.EqualFold(strings.TrimSpace(typed), acct.Email) {
		return errors.New("the confirmation does not match the account email; nothing was changed")
	}
	return nil
}

// find loads the account in its own short transaction.
func (e *recoverEnv) find(ctx context.Context, email string) (*auth.OperatorAccount, error) {
	var acct *auth.OperatorAccount
	err := e.svc.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		acct, err = e.svc.OperatorFind(ctx, tx, email)
		return err
	})
	return acct, err
}

// recordOperatorAudit records one system action in every team the account belongs to (or the
// oldest team for an account with none). It never records a token or password.
func recordOperatorAudit(ctx context.Context, tx pgx.Tx, acct *auth.OperatorAccount, action string, md map[string]any) error {
	actor := auth.WithPrincipal(ctx, &auth.Principal{Type: auth.TypeSystem, Label: operatorLabel})
	meta := map[string]any{"email": acct.Email, "via": "cli"}
	if u, err := user.Current(); err == nil {
		meta["osUser"] = u.Username
	}
	for k, v := range md {
		meta[k] = v
	}
	teams := acct.Teams
	if len(teams) == 0 {
		teams = []uuid.UUID{uuid.Nil}
	}
	for _, team := range teams {
		if err := audit.Record(actor, tx, audit.Entry{
			Action: action, ResourceType: "user", ResourceID: acct.ID.String(), WorkspaceID: team, Metadata: meta,
		}); err != nil {
			return err
		}
	}
	return nil
}

// endSessions signs the account out everywhere. It runs after the change has
// committed; a failure is reported but the change stays.
func (e *recoverEnv) endSessions(acct *auth.OperatorAccount) error {
	if err := auth.RevokeUserSessions(e.sessions, acct.ID); err != nil {
		return fmt.Errorf("the change was made, but ending the account's sessions failed: %w", err)
	}
	fmt.Fprintln(e.out, "All browser sessions of the account were ended.")
	return nil
}

type resetPasswordOpts struct {
	email    string
	generate bool
	confirm  string
}

// resetPassword issues a one-time reset link, or (generate) sets a new random
// password and shows it once.
func (e *recoverEnv) resetPassword(ctx context.Context, o resetPasswordOpts) error {
	acct, err := e.find(ctx, o.email)
	if err != nil {
		return err
	}
	if o.generate {
		return e.generatePassword(ctx, acct, o.confirm)
	}
	var raw string
	var expires time.Time
	err = e.svc.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		raw, expires, err = e.svc.OperatorResetLink(ctx, tx, acct)
		if err != nil {
			return err
		}
		return recordOperatorAudit(ctx, tx, acct, "operator.password_reset_link", map[string]any{"expiresAt": expires.UTC().Format(time.RFC3339)})
	})
	if err != nil {
		return err
	}
	link := e.svc.ResetLink(raw)
	fmt.Fprintf(e.out, "Account: %s\n", acct.Email)
	fmt.Fprintf(e.out, "One-time password reset link (works once, expires %s):\n\n", expires.Local().Format("15:04 MST"))
	if link != "" {
		fmt.Fprintf(e.out, "  %s\n\n", link)
	} else {
		fmt.Fprintf(e.out, "  <your dashboard URL>/reset-password?token=%s\n\n", raw)
		fmt.Fprintln(e.out, "NEBULACTRL_PUBLIC_URL is not set, so the link has no host: put the address you open the dashboard at in front of it.")
	}
	fmt.Fprintln(e.out, "Open it in a browser and choose a new password. Two-step verification still applies at the next sign-in; if that is lost too, run reset-2fa.")
	return e.endSessions(acct)
}

func (e *recoverEnv) generatePassword(ctx context.Context, acct *auth.OperatorAccount, confirm string) error {
	fmt.Fprintf(e.out, "This replaces the password of %s with a generated one. The old password stops working.\n", acct.Email)
	if err := e.confirmEmail(acct, confirm); err != nil {
		return err
	}
	password := rand.Text()
	err := e.svc.InTx(ctx, func(tx pgx.Tx) error {
		if err := e.svc.OperatorSetPassword(ctx, tx, acct, password); err != nil {
			return err
		}
		return recordOperatorAudit(ctx, tx, acct, "operator.password_set", map[string]any{"hadPassword": acct.HasPassword})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "\nNew password for %s (shown once; change it in Account settings -> Security after signing in):\n\n  %s\n\n", acct.Email, password)
	fmt.Fprintln(e.out, "Two-step verification still applies at the next sign-in; if that is lost too, run reset-2fa.")
	return e.endSessions(acct)
}

// reset2FA removes the account's authenticator app, passkeys and recovery codes.
func (e *recoverEnv) reset2FA(ctx context.Context, email, confirm string) error {
	acct, err := e.find(ctx, email)
	if err != nil {
		return err
	}
	var held auth.SecondFactorSummary
	err = e.svc.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		held, err = e.svc.OperatorSecondFactors(ctx, tx, acct)
		return err
	})
	if err != nil {
		return err
	}
	if held.None() {
		fmt.Fprintf(e.out, "%s has no authenticator app, passkey or recovery code: nothing to reset.\n", acct.Email)
		return nil
	}
	fmt.Fprintf(e.out, "Account %s holds: authenticator app %s, %d passkey(s), %d unused recovery code(s).\n", acct.Email, yesNo(held.TOTP), held.Passkeys, held.RecoveryCodes)
	fmt.Fprintln(e.out, "All of them will be removed. The account will sign in with its password (or provider) alone until it enrols again.")
	if err := e.confirmEmail(acct, confirm); err != nil {
		return err
	}
	err = e.svc.InTx(ctx, func(tx pgx.Tx) error {
		cleared, err := e.svc.OperatorClearSecondFactors(ctx, tx, acct)
		if err != nil {
			return err
		}
		held = cleared
		return recordOperatorAudit(ctx, tx, acct, "operator.2fa_reset", map[string]any{
			"totp": cleared.TOTP, "passkeys": cleared.Passkeys, "recoveryCodes": cleared.RecoveryCodes,
		})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "\nRemoved: authenticator app %s, %d passkey(s), %d recovery code(s).\n", yesNo(held.TOTP), held.Passkeys, held.RecoveryCodes)
	fmt.Fprintln(e.out, "Sign in and enrol again in Account settings -> Security. A team that requires 2FA asks for it at the next sign-in.")
	return e.endSessions(acct)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// resetPasswordCmd is the lost-password half of operator recovery.
func resetPasswordCmd() *cobra.Command {
	var o resetPasswordOpts
	cmd := &cobra.Command{
		Use:   "reset-password <email>",
		Short: "Print a one-time password reset link for an account (run on the control-plane host)",
		Long: `Recover an account whose password is lost, from the control-plane host.

It connects to the database directly, like rekey, so run it where the server's
environment is set (NEBULACTRL_DATABASE_URL, NEBULACTRL_SECRET_KEY and
NEBULACTRL_PUBLIC_URL), for example:

  docker compose exec nebulactrl nebulactrl-server reset-password you@example.com

By default it prints a one-time link (valid 30 minutes) that opens the normal
"choose a new password" page. No SMTP relay is needed.

With --generate it instead sets a new random password and shows it once; that
also works for an account that has no password yet (passkey or provider only).
It asks you to type the account email to confirm (or pass --confirm).

Either way every browser session of the account is ended and the action is
audited as a system action. Two-step verification still applies at the next
sign-in; use reset-2fa if that is lost too. API tokens are not revoked.

If the instance has no accounts yet, first-run setup is still pending: use the
setup URL the installer printed instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			env, closeEnv, err := openRecoverEnv(ctx, cmd)
			if err != nil {
				return err
			}
			defer closeEnv()
			o.email = args[0]
			return env.resetPassword(ctx, o)
		},
	}
	cmd.Flags().BoolVar(&o.generate, "generate", false, "set a generated password and show it once instead of printing a link")
	cmd.Flags().StringVar(&o.confirm, "confirm", "", "with --generate: the account email, instead of typing it when asked")
	return cmd
}

// reset2FACmd is the lost-second-factor half of operator recovery.
func reset2FACmd() *cobra.Command {
	var confirm string
	cmd := &cobra.Command{
		Use:   "reset-2fa <email>",
		Short: "Remove an account's authenticator app, passkeys and recovery codes (run on the control-plane host)",
		Long: `Recover an account whose second factor is lost, from the control-plane host.

It connects to the database directly, like rekey (same environment as the
server). It removes the account's authenticator app, every passkey and every
recovery code after you type the account email to confirm (or pass --confirm).
The account then signs in with its password or provider alone until it enrols
again; a team that requires 2FA asks for that at the next sign-in. Every browser
session of the account is ended and the action is audited as a system action.

It refuses when that would leave an account with no way in (no password, no
linked provider, passkeys only): run reset-password --generate first.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			env, closeEnv, err := openRecoverEnv(ctx, cmd)
			if err != nil {
				return err
			}
			defer closeEnv()
			return env.reset2FA(ctx, args[0], confirm)
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the account email, instead of typing it when asked")
	return cmd
}
