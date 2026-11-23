package projectconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The file refuses more than one replica for what it declares to run as a single instance, the
// way services.Eligibility does at run time.
func TestScaleEligibilityDeclaredInTheFile(t *testing.T) {
	for name, c := range map[string]struct {
		body    string
		want    bool // a replicas_require_stateless error
		message string
	}{
		"files": {
			"[services.web]\nimage = \"nginx:1\"\nport = 80\n\n[services.web.files.\"/etc/a.conf\"]\ncontent = \"x\"\n\n[services.web.scale]\nreplicas = 2\n",
			true, "config files",
		},
		"files, autoscaling": {
			"[services.web]\nimage = \"nginx:1\"\nport = 80\n\n[services.web.files.\"/etc/a.conf\"]\ncontent = \"x\"\n\n[services.web.scale.autoscaling]\nenabled = true\nmax = 4\n",
			true, "config files",
		},
		"files, one replica": {
			"[services.web]\nimage = \"nginx:1\"\nport = 80\n\n[services.web.files.\"/etc/a.conf\"]\ncontent = \"x\"\n\n[services.web.scale]\nreplicas = 1\n",
			false, "",
		},
		"port 0": {
			"[services.web]\nimage = \"nginx:1\"\nport = 0\n\n[services.web.scale]\nreplicas = 3\n",
			true, "without an HTTP port",
		},
		"port 0 with processes": {
			"[services.web]\nimage = \"nginx:1\"\nport = 0\n\n[services.web.processes.worker]\ncommand = [\"run\"]\n\n[services.web.processes.worker.scale]\nreplicas = 3\n",
			false, "",
		},
		"port left out": {
			"[services.web]\nimage = \"nginx:1\"\n\n[services.web.scale]\nreplicas = 3\n",
			false, "",
		},
		"process of a service with files": {
			"[services.web]\nimage = \"nginx:1\"\nport = 80\n\n[services.web.files.\"/etc/a.conf\"]\ncontent = \"x\"\n\n[services.web.processes.worker]\ncommand = [\"run\"]\n\n[services.web.processes.worker.scale]\nreplicas = 2\n",
			true, "config files",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, ds := Parse([]byte(t2doc(c.body)), nil)
			var got *Diagnostic
			for i := range ds {
				if ds[i].Code == CodeReplicasRequireStateles {
					got = &ds[i]
				}
			}
			if !c.want {
				require.Nil(t, got, ds.RenderText(""))
				return
			}
			require.NotNil(t, got, ds.RenderText(""))
			require.Contains(t, got.Message, c.message)
			require.Equal(t, SeverityError, got.Severity)
		})
	}
}
