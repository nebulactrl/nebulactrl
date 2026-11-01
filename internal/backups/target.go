package backups

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// target is a database as the agent needs to address it. Password is the only
// secret; it travels in Command.Secrets.
type target struct {
	Engine   string
	Version  string
	Database string
	User     string
	Password string
}

func (t target) proto(serviceID string) agentprotocol.BackupTarget {
	return agentprotocol.BackupTarget{ServiceID: serviceID, Engine: t.Engine, Version: t.Version, Database: t.Database, User: t.User}
}

// databaseSpec reads services.config.database of a service.
func databaseSpec(raw json.RawMessage) (*engines.Spec, error) {
	var c struct {
		Database *engines.Spec `json:"database"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.Database == nil {
		return nil, apierr.Unprocessable("not_a_managed_database", "backups are available for managed databases only")
	}
	spec := c.Database.Normalize()
	return &spec, nil
}

// supported reports whether engine can be backed up and whether it can be
// restored.
func supported(engine string) (backupOK, restoreOK bool) {
	return agentprotocol.BackupSupported(engine), agentprotocol.RestoreSupported(engine)
}

// resolveTarget determines engine, version, database and credentials of a
// managed database from its config and resolved variables.
func (s *Service) resolveTarget(ctx context.Context, q *db.Queries, sc services.Scope) (target, error) {
	spec, err := databaseSpec(sc.Service.Config)
	if err != nil {
		return target{}, err
	}
	if spec.Replica != nil {
		// A standby's pg_dump can be cancelled by recovery conflicts: backups run on
		// the group's current primary only (schedules follow it on Promote).
		return target{}, apierr.Unprocessable("backup_replica_unsupported",
			"this database is a read replica: back up its primary instead (backups run on the current primary only)")
	}
	eng := engines.Lookup(spec.Engine)
	if eng == nil {
		return target{}, apierr.Unprocessable("backup_unsupported", fmt.Sprintf("unknown database engine %q", spec.Engine))
	}
	if ok, _ := supported(spec.Engine); !ok {
		return target{}, apierr.Unprocessable("backup_unsupported", fmt.Sprintf("backups are not supported for %s yet", eng.Label))
	}
	rs, err := services.LoadResolver(ctx, q, s.box, sc.EnvInfo())
	if err != nil {
		return target{}, fmt.Errorf("load variables: %w", err)
	}
	res, err := s.ext.ResolveService(ctx, rs, sc.ExtScope(services.ExtPurposeBackup), sc.Service.Slug)
	if err != nil {
		return target{}, fmt.Errorf("resolve variables: %w", err)
	}
	creds := eng.Creds(*spec, func(k string) (string, bool) { v, ok := res.Vars[k]; return v, ok })
	t := target{Engine: spec.Engine, Version: spec.Version, Database: creds.Database, User: creds.User, Password: creds.Password}
	switch spec.Engine {
	case engines.MySQL, engines.MariaDB:
		// The app user cannot dump routines, events and triggers; root can.
		t.User, t.Password = "root", creds.RootPassword
	case engines.Valkey, engines.Redis:
		t.User, t.Database = "", ""
	case engines.MongoDB:
		t.Database = ""
	}
	return t, nil
}

// objectExt is the file extension of an engine's dump.
func objectExt(engine string) string {
	switch engine {
	case engines.Postgres:
		return ".dump"
	case engines.MySQL, engines.MariaDB:
		return ".sql.gz"
	case engines.MongoDB:
		return ".archive.gz"
	default:
		return ".rdb"
	}
}
