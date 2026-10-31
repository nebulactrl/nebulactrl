package fleet

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// A service whose Docker options need an unrestricted server is never moved
// to a restricted one automatically, and an explicit restricted target is
// reported as blocking (the deploy would be refused anyway).
func TestDrainPlanKeepsUnrestrictedServicesOnUnrestrictedServers(t *testing.T) {
	src, plain, lab := uuid.New(), uuid.New(), uuid.New()
	host := func(id uuid.UUID, name string, unrestricted bool) *hostInfo {
		return &hostInfo{srv: db.Server{ID: id, Name: name, Status: "online", AcceptWorkloads: true, DrainState: "none", Unrestricted: unrestricted}, status: "online"}
	}
	sn := &snapshot{
		hosts: map[uuid.UUID]*hostInfo{src: host(src, "src", true), plain: host(plain, "plain", false), lab: host(lab, "lab", true)},
		order: []uuid.UUID{src, plain, lab},
		services: []db.ListFleetServicesRow{
			{ID: uuid.New(), Name: "gpu", ServerID: &src, Config: []byte(`{"image":"x:1","docker":{"gpus":{"count":-1,"driver":"nvidia"},"devices":[{"hostPath":"/dev/kfd"}]}}`)},
			{ID: uuid.New(), Name: "web", ServerID: &src, Config: []byte(`{"image":"x:1","docker":{"capDrop":["ALL"]}}`)},
		},
		samples: map[uuid.UUID]db.LatestServiceMetricSamplesRow{},
	}
	s := &Service{}
	byName := func(p DrainPlan) map[string]PlanMove {
		m := map[string]PlanMove{}
		for _, mv := range p.Moves {
			m[mv.ServiceName] = mv
		}
		return m
	}

	// Automatic choice: the GPU service only considers the unrestricted server.
	moves := byName(s.plan(sn, src, nil, nil))
	require.NotNil(t, moves["gpu"].TargetServerID)
	require.Equal(t, "lab", moves["gpu"].TargetServerName)
	require.Empty(t, moves["gpu"].Blocking)

	// Only restricted servers are left: nothing to pick, so it blocks.
	sn.hosts[lab].srv.Unrestricted = false
	moves = byName(s.plan(sn, src, nil, nil))
	require.Nil(t, moves["gpu"].TargetServerID)
	require.Equal(t, BlockNoTarget, moves["gpu"].Blocking[0].Code)
	require.Empty(t, moves["web"].Blocking, "the ungated tier moves anywhere")

	// An explicit restricted target is planned but blocked, naming the options.
	target := plain
	moves = byName(s.plan(sn, src, &target, nil))
	require.Equal(t, "plain", moves["gpu"].TargetServerName)
	require.Len(t, moves["gpu"].Blocking, 1)
	require.Equal(t, BlockRequiresUnrestricted, moves["gpu"].Blocking[0].Code)
	require.Contains(t, moves["gpu"].Blocking[0].Message, "devices")
	require.Contains(t, moves["gpu"].Blocking[0].Message, "gpus")
	require.Empty(t, moves["web"].Blocking)
}
