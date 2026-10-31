// Package agentdeploy embeds the agent installation assets served by the
// control plane.
package agentdeploy

import _ "embed"

// InstallScriptTemplate is the text/template source of /install-agent.sh.
//
//go:embed install.sh.tmpl
var InstallScriptTemplate string
