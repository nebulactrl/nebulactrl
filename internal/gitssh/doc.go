// Package gitssh is the SSH side of git sources: deploy-key generation and
// parsing, ssh:// and scp-style URL parsing, known_hosts pinning (trust on first
// use) and the go-git auth method that enforces the pins.
//
// It has no database or HTTP code and is imported by the agent (clone) as well
// as the control plane (ls-remote, key management); `deploykeys` is the control
// plane's service on top of it.
package gitssh
