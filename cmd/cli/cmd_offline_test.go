package main

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// offlineArgs builds a plausible command line for a leaf command from its Use
// string: one placeholder per <argument>, KEY=VALUE for assignments.
func offlineArgs(cmd *cobra.Command) []string {
	var args []string
	for p := cmd; p.HasParent(); p = p.Parent() {
		args = append([]string{p.Name()}, args...)
	}
	fields := strings.Fields(cmd.Use)
	for _, tok := range fields[1:] {
		switch {
		case tok == "--":
			args = append(args, "--")
		case strings.HasPrefix(tok, "[") || strings.HasPrefix(tok, "-"):
		case strings.Contains(tok, "KEY=VALUE"):
			args = append(args, "K=v")
		case strings.Contains(tok, "|") && !strings.HasPrefix(tok, "<"):
			args = append(args, strings.Split(tok, "|")[0])
		default:
			args = append(args, "x")
		}
	}
	return args
}

// Commands that work without a control plane, or that read the project file
// (and so complain about it) before they need one.
var runsWithoutLogin = map[string]bool{
	"login": true, "docs": true, "teams switch": true, "config validate": true, "config init": true, "config schema": true,
	"config plan": true, "config apply": true,
}

// TestEveryAPICommandRefusesWithoutALogin walks the whole command tree with no
// saved login: a command that talks to the control plane must say so and exit 1
// (or reject its arguments with exit 2); it must never fall through to a
// request, a panic or a hang.
func TestEveryAPICommandRefusesWithoutALogin(t *testing.T) {
	var leaves []*cobra.Command
	walkCLI(newRoot(&cli{}), func(cmd *cobra.Command) {
		if !cmd.HasSubCommands() && cmd.HasParent() {
			leaves = append(leaves, cmd)
		}
	})
	require.Greater(t, len(leaves), 100, "the command tree was not walked")

	var bad []string
	for _, cmd := range leaves {
		path := strings.TrimPrefix(cmd.CommandPath(), "nebulactrl ")
		if runsWithoutLogin[path] {
			continue
		}
		cfgEnv(t)
		t.Chdir(t.TempDir())
		var out, errOut bytes.Buffer
		c := &cli{in: strings.NewReader(""), out: &out, errOut: &errOut, pollEvery: time.Millisecond}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		code := c.execute(ctx, offlineArgs(cmd))
		cancel()
		switch {
		case code == 2:
		case code == 1 && strings.Contains(errOut.String(), "not logged in: run `nebulactrl login --url <url> --token <token>`"):
		default:
			bad = append(bad, fmt.Sprintf("%s: exit %d %s", path, code, strings.TrimSpace(errOut.String())))
		}
	}
	sort.Strings(bad)
	require.Empty(t, bad, "commands that neither refuse nor reject their arguments:\n%s", strings.Join(bad, "\n"))
}
