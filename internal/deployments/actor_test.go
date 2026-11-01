package deployments

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

func TestActorDisplay(t *testing.T) {
	labels := map[actorKey]string{
		{"user", "u1"}:  "Alice",
		{"token", "t1"}: "ci-deploy",
	}
	cases := []struct {
		name string
		dep  db.Deployment
		want string
	}{
		{"user", db.Deployment{ActorType: "user", ActorID: "u1"}, "Alice"},
		{"token", db.Deployment{ActorType: "token", ActorID: "t1"}, "ci-deploy"},
		{"mcp", db.Deployment{ActorType: "mcp", ActorID: "t1"}, "MCP · ci-deploy"},
		{"system", db.Deployment{ActorType: "system"}, "System"},
		{"reconcile without a principal", db.Deployment{ActorType: "system", Trigger: TriggerReconcile}, "System"},
		{"deleted user", db.Deployment{ActorType: "user", ActorID: "gone"}, "Removed user"},
		{"deleted token", db.Deployment{ActorType: "token", ActorID: "gone"}, "Removed token"},
		{"deleted mcp token", db.Deployment{ActorType: "mcp", ActorID: "gone"}, "MCP"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, actorDisplay(c.dep, labels))
		})
	}
}
