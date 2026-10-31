package backups

import (
	"context"
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

const guardRestore = "restore_backup"

// Operation names of the guarded backup changes. They are the catalog's
// operation ids, so an approval reads the same over REST and MCP.
const (
	guardDeleteBackup           = "deleteBackup"
	guardUnlockBackup           = "lockBackup"
	guardDeleteVolumeBackup     = "deleteVolumeBackup"
	guardUnlockVolumeBackup     = "lockVolumeBackup"
	guardDeleteRetiredVolume    = "deleteRetiredVolume"
	guardCreateSchedule         = "createBackupSchedule"
	guardUpdateSchedule         = "updateBackupSchedule"
	guardDeleteSchedule         = "deleteBackupSchedule"
	guardPutVolumeBackupConfig  = "putVolumeBackupConfig"
	guardDeleteVolumeBackupConf = "deleteVolumeBackupConfig"
)

// guardRestoreCall asks the production guard before a restore touches or adds a
// production database. It returns a context that covers the follow-up calls the
// approved restore makes itself (deploying the new database).
func guardRestoreCall(ctx context.Context, sc services.Scope, b db.Backup, mode, newName string, in RestoreInput) (context.Context, error) {
	if !prodguard.Applies(ctx, sc.IsProduction) {
		return ctx, nil
	}
	var summary string
	var eff []string
	irreversible := false
	switch mode {
	case ModeInPlace:
		summary = fmt.Sprintf("Restore %s in production from a backup, overwriting its data (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName)
		eff = []string{
			"Overwrites the current data of " + sc.Service.Name + " with the backup from " + b.CreatedAt.UTC().Format("2006-01-02 15:04 UTC") + ".",
			"A fresh backup of the current data is taken first.",
		}
		irreversible = true
	default:
		summary = fmt.Sprintf("Restore a backup of %s into a new database (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName)
		eff = []string{
			"Creates the database " + newName + " next to " + sc.Service.Name + " and deploys it.",
			"The existing database is not touched.",
		}
	}
	extra := ""
	if in.ServerID != nil {
		extra = in.ServerID.String()
	}
	if err := prodguard.Check(ctx, sc.ProdScope(summary, irreversible, eff...), guardRestore,
		prodguard.Digest(guardRestore, b.ID, mode, in.Name, extra)); err != nil {
		return ctx, err
	}
	return prodguard.Covered(ctx), nil
}
