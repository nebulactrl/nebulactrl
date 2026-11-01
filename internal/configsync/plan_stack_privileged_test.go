package configsync

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// An unrestricted server lifts the compose allow-list. A stack file that uses
// host-level compose options is flagged like a privileged docker option, so an
// automatic apply refuses it and a person applies it.
func TestPlanDB_PrivilegedComposeIsFlaggedForAutoApply(t *testing.T) {
	f := newPlanFixture(t)
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET unrestricted = true WHERE id = $1", f.server.ID)
	require.NoError(t, err)

	stack := func(service string) string {
		return planHeadServer + "\n[services.shop.stack]\ncompose = '''\nservices:\n  web:\n" + service + "'''\n"
	}

	file := stack("    image: nginx:1.27\n    privileged: true\n")
	p := f.plan(f.staging, file)
	require.True(t, p.Valid, "%v", p.Diagnostics)
	d := planDiag(t, p.Diagnostics, "auto_apply_privileged")
	require.Equal(t, "info", d.Severity)
	require.Equal(t, "services.shop.stack.compose", d.Path)

	for name, opts := range map[string]string{
		"pid host":      "    image: nginx:1.27\n    pid: host\n",
		"devices":       "    image: nginx:1.27\n    devices:\n      - /dev/fuse\n",
		"cap NET_ADMIN": "    image: nginx:1.27\n    cap_add:\n      - NET_ADMIN\n",
	} {
		p = f.plan(f.staging, stack(opts))
		require.True(t, p.Valid, "%s: %v", name, p.Diagnostics)
		require.True(t, p.Diagnostics.HasCode("auto_apply_privileged"), name)
	}

	// An ordinary stack is not flagged, on an unrestricted server either.
	p = f.plan(f.staging, stack("    image: nginx:1.27\n"))
	require.True(t, p.Valid, "%v", p.Diagnostics)
	require.False(t, p.Diagnostics.HasCode("auto_apply_privileged"))
}
