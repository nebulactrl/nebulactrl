package variables

import (
	"context"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// RunTemplateScanJob runs the template scan worker the way River would for one
// attempt of a job for serviceID.
func RunTemplateScanJob(ctx context.Context, s *Service, serviceID uuid.UUID) error {
	w := &envTemplateScanWorker{s: s}
	return w.Work(ctx, &river.Job[EnvTemplateScanArgs]{Args: EnvTemplateScanArgs{ServiceID: serviceID}})
}
