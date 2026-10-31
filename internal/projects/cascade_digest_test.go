package projects

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The cascade approval is bound to what the approver saw, not just to the
// project id: anything added in the ten-minute window asks again.
func TestCascadeDigestBindsThePlan(t *testing.T) {
	pid, prod := uuid.New(), uuid.New()
	plan := DeletionPlan{
		Project:      PlanProject{ID: pid},
		Environments: []PlanEnvironment{{ID: prod, Production: true}},
		Services:     2, Databases: 1, Volumes: 3, Domains: 2, Previews: 0,
	}
	d := cascadeDigest(plan)
	require.Equal(t, d, cascadeDigest(plan), "the same plan gives the same digest")

	for name, mutate := range map[string]func(*DeletionPlan){
		"a service was added":     func(p *DeletionPlan) { p.Services++ },
		"a database was added":    func(p *DeletionPlan) { p.Databases++ },
		"a volume was added":      func(p *DeletionPlan) { p.Volumes++ },
		"a domain was added":      func(p *DeletionPlan) { p.Domains++ },
		"a preview appeared":      func(p *DeletionPlan) { p.Previews++ },
		"an environment appeared": func(p *DeletionPlan) { p.Environments = append(p.Environments, PlanEnvironment{ID: uuid.New()}) },
		"another project":         func(p *DeletionPlan) { p.Project.ID = uuid.New() },
	} {
		changed := plan
		changed.Environments = append([]PlanEnvironment(nil), plan.Environments...)
		mutate(&changed)
		require.NotEqual(t, d, cascadeDigest(changed), name)
	}
}
