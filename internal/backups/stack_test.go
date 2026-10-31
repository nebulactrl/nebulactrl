package backups

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func TestVolumeBackupsAreNotAvailableForStacks(t *testing.T) {
	sc := services.Scope{}
	sc.Service.Kind = services.KindStack
	require.Equal(t, "Not available for Compose stacks", volumeIneligibleReason(sc, []db.Volume{{}}))
}
