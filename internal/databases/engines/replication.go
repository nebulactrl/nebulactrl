package engines

// PostgreSQL read replicas.
//
// Everything here is pure: argv vectors, constant SQL and constant shell
// scripts. The agent runs them; nothing is built from request data except
// validated identifiers, which travel in the environment of the command (psql
// \getenv / sh variables), never in the SQL or script text. No plan carries a
// secret in argv or stdin: the new role password rides in the environment.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Environment variable names of the replication plans.
const (
	envReplRole = "NEBULA_REPL_ROLE"
	envReplSlot = "NEBULA_REPL_SLOT"
	envReplLSN  = "NEBULA_REPL_LSN"
)

// ReplArgs are the arguments of ReplPlan, HBAPlan and ReconfigurePlan. It holds
// no secret except NewPassword (repl_prepare), which only ever lands in the
// environment of the exec.
type ReplArgs struct {
	// Role is the replication role (nebula_r_<replicaId8>), Slot the physical
	// slot and application_name (nc_r_<replicaId8>).
	Role string
	Slot string
	// MaxSlotWALKeepMB is max_slot_wal_keep_size in MB (repl_prepare); 0 leaves it.
	MaxSlotWALKeepMB int
	// LSN is the position repl_promote requires the replica to have replayed.
	LSN string
	// Host and Port are what the replica dials (repl_reconfigure).
	Host string
	Port int
	// NewPassword is the replication role's password (repl_prepare).
	NewPassword string
}

// HBA markers and address of the managed pg_hba.conf block.
const (
	HBABegin = "# BEGIN nebulactrl-replication"
	HBAEnd   = "# END nebulactrl-replication"
	// HBAAddress limits replication logins to the subnets the primary is
	// attached to: a same-server replica (a sibling container) and the mesh
	// server node. Per-replica role + scram is the identity.
	HBAAddress = "samenet"
)

// HBALine is the pg_hba.conf line of one replication role.
func HBALine(role string) string {
	return "host replication " + role + " " + HBAAddress + " scram-sha-256"
}

var (
	replHostRe   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	replLSNRe    = regexp.MustCompile(`^[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}$`)
	replVolumeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
)

// ValidReplHost reports whether h is a host name or address the replica may
// dial (a service slug, a mesh alias or an IP).
func ValidReplHost(h string) bool { return replHostRe.MatchString(h) }

// ValidLSN reports whether s is a PostgreSQL pg_lsn literal.
func ValidLSN(s string) bool { return replLSNRe.MatchString(s) }

// ValidVolumeName reports whether v is a safe unprefixed volume name.
func ValidVolumeName(v string) bool { return replVolumeRe.MatchString(v) }

// replPSQL is the psql argv of every replication statement: the local trust
// socket, the maintenance database, no password.
func replPSQL(user string) []string {
	return []string{"psql", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-U", user, "-d", "postgres"}
}

// SQL of the replication actions. Constants: identifiers arrive through \getenv.
const (
	// replPrepareSQL creates (or re-keys) the replication role.
	replPrepareSQL = `\getenv nebula_role NEBULA_REPL_ROLE
\getenv nebula_pw NEBULA_DB_NEW_PASSWORD
SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'nebula_role')
  THEN format('ALTER ROLE %I WITH REPLICATION LOGIN PASSWORD %L', :'nebula_role', :'nebula_pw')
  ELSE format('CREATE ROLE %I WITH REPLICATION LOGIN PASSWORD %L', :'nebula_role', :'nebula_pw') END \gexec
`
	// replReloadSQL applies pg_hba.conf and sighup settings.
	replReloadSQL = "SELECT pg_reload_conf();\n"

	// replDropSQL ends the role's sessions, drops the slot and the role.
	replDropSQL = `\getenv nebula_role NEBULA_REPL_ROLE
\getenv nebula_slot NEBULA_REPL_SLOT
SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE usename = :'nebula_role';
SELECT pg_sleep(1);
SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = :'nebula_slot';
SELECT format('DROP ROLE IF EXISTS %I', :'nebula_role') \gexec
SELECT pg_reload_conf();
`

	// ReplFreezeSQL makes new transactions read-only, ends client sessions and
	// checkpoints. It prints "nebula:lsn:<position>" last.
	ReplFreezeSQL = `ALTER SYSTEM SET default_transaction_read_only = on;
SELECT pg_reload_conf();
SELECT pg_sleep(1);
SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE backend_type = 'client backend' AND pid <> pg_backend_pid();
CHECKPOINT;
SELECT 'nebula:lsn:' || pg_current_wal_lsn();
`

	// ReplUnfreezeSQL reverts ReplFreezeSQL.
	ReplUnfreezeSQL = `ALTER SYSTEM RESET default_transaction_read_only;
SELECT pg_reload_conf();
`

	// ReplPromoteSQL promotes a standby that has replayed up to
	// NEBULA_REPL_LSN (empty: no requirement), then forgets the upstream. A
	// server that is no longer in recovery is reported as promoted (re-entrant).
	// It prints "nebula:lsn:<position>" last.
	ReplPromoteSQL = `\getenv nebula_lsn NEBULA_REPL_LSN
SELECT pg_is_in_recovery() AS in_recovery,
       (:'nebula_lsn' = '' OR pg_last_wal_replay_lsn() >= NULLIF(:'nebula_lsn', '')::pg_lsn) AS caught_up \gset
\if :in_recovery
  \if :caught_up
    SELECT pg_promote(true, 60) AS promoted \gset
    \if :promoted
      ALTER SYSTEM RESET primary_slot_name;
      ALTER SYSTEM RESET primary_conninfo;
      SELECT pg_reload_conf();
      SELECT 'nebula:lsn:' || pg_current_wal_lsn();
    \else
      \warn pg_promote did not finish within 60 seconds
      SELECT 1/0;
    \endif
  \else
    \warn the replica has not replayed up to the requested position
    SELECT 1/0;
  \endif
\else
  SELECT 'nebula:lsn:' || pg_current_wal_lsn();
\endif
`

	// ReplStatusSQL prints one JSON line (see ParseReplStatus). The role query
	// decides which side is read, so pg_current_wal_lsn() never runs on a standby.
	ReplStatusSQL = `SELECT pg_is_in_recovery() AS in_recovery \gset
\if :in_recovery
  SELECT json_build_object(
    'role', 'replica',
    'inRecovery', true,
    'receiverStatus', COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1), 'stopped'),
    'receiveLsn', pg_last_wal_receive_lsn()::text,
    'replayLsn', pg_last_wal_replay_lsn()::text,
    'replayLagBytes', COALESCE(pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()), 0)::bigint);
\else
  SELECT json_build_object(
    'role', 'primary',
    'inRecovery', false,
    'slots', COALESCE((SELECT json_agg(json_build_object(
        'name', slot_name, 'active', active, 'walStatus', wal_status, 'safeWalSize', safe_wal_size))
      FROM pg_replication_slots WHERE slot_type = 'physical'), '[]'::json),
    'standbys', COALESCE((SELECT json_agg(json_build_object(
        'applicationName', application_name, 'state', state, 'syncState', sync_state,
        'lagBytes', COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn), 0)::bigint,
        'replayLagMs', (EXTRACT(EPOCH FROM replay_lag) * 1000)::bigint))
      FROM pg_stat_replication), '[]'::json));
\endif
`
)

// ErrNotPostgres is returned by the replication plans for other engines.
var ErrNotPostgres = errors.New("read replicas are only supported for PostgreSQL")

func replSuperuser(s Spec) (string, error) {
	if s.Engine != Postgres {
		return "", ErrNotPostgres
	}
	user := s.Normalize().Username
	if !ValidIdent(user) {
		return "", errors.New("invalid database user")
	}
	return user, nil
}

// ReplPlan builds the command of a replication db_admin action
// (agentprotocol.DBActionRepl*): psql with constant SQL, except repl_final_lsn,
// whose plan is pg_controldata for a one-off container (see ControlDataScript).
// Actions with more than one step have their other steps in HBAPlan,
// ReconfigurePlan and PromoteCleanupPlan.
func ReplPlan(s Spec, action string, a ReplArgs) (ExecPlan, error) {
	user, err := replSuperuser(s)
	if err != nil {
		return ExecPlan{}, err
	}
	plan := ExecPlan{Argv: replPSQL(user)}
	switch action {
	case agentprotocol.DBActionReplPrepare:
		if !ValidIdent(a.Role) {
			return ExecPlan{}, errors.New("invalid replication role")
		}
		if !ValidNewPassword(a.NewPassword) {
			return ExecPlan{}, errors.New("replication password must be 16-128 characters of [A-Za-z0-9_-]")
		}
		if a.MaxSlotWALKeepMB < 0 || a.MaxSlotWALKeepMB > 1<<20 {
			return ExecPlan{}, errors.New("max_slot_wal_keep_size out of range")
		}
		plan.Env = []string{envReplRole + "=" + a.Role, envNewPassword + "=" + a.NewPassword}
		plan.Stdin = replPrepareSQL
		if a.MaxSlotWALKeepMB > 0 {
			plan.Stdin += "ALTER SYSTEM SET max_slot_wal_keep_size = '" + strconv.Itoa(a.MaxSlotWALKeepMB) + "MB';\n"
		}
		plan.Stdin += replReloadSQL
	case agentprotocol.DBActionReplDrop:
		if !ValidIdent(a.Role) || !ValidIdent(a.Slot) {
			return ExecPlan{}, errors.New("invalid replication role or slot")
		}
		plan.Env = []string{envReplRole + "=" + a.Role, envReplSlot + "=" + a.Slot}
		plan.Stdin = replDropSQL
	case agentprotocol.DBActionReplFreeze:
		plan.Stdin = ReplFreezeSQL
	case agentprotocol.DBActionReplUnfreeze:
		plan.Stdin = ReplUnfreezeSQL
	case agentprotocol.DBActionReplPromote:
		if a.LSN != "" && !ValidLSN(a.LSN) {
			return ExecPlan{}, errors.New("invalid LSN")
		}
		plan.Env = []string{envReplLSN + "=" + a.LSN}
		plan.Stdin = ReplPromoteSQL
	case agentprotocol.DBActionReplReconfigure:
		plan.Stdin = replReloadSQL // after ReconfigurePlan rewrote the include file
	case agentprotocol.DBActionReplStatus:
		plan.Stdin = ReplStatusSQL
	case agentprotocol.DBActionReplFinalLSN:
		// Not a psql statement: the agent runs this argv in a one-off container
		// of the stopped primary's image with its data volume mounted read-only
		// (the server is down, so there is nothing to exec in). Read the result
		// with ParseControlData.
		plan = ExecPlan{Argv: []string{"sh", "-c", ControlDataScript}}
	default:
		return ExecPlan{}, fmt.Errorf("unsupported replication action %q", action)
	}
	return plan, nil
}

// hbaScript adds or removes one line of the managed pg_hba.conf block, as user
// postgres. The block is rebuilt in a temporary file that replaces pg_hba.conf
// atomically; an empty block disappears with its markers. Idempotent.
const hbaScript = `set -eu
umask 077
f="$PGDATA/pg_hba.conf"
[ -f "$f" ] || { echo "pg_hba.conf not found in $PGDATA" >&2; exit 4; }
B='` + HBABegin + `'
E='` + HBAEnd + `'
rest=$(awk -v b="$B" -v e="$E" '$0==b{s=1;next} $0==e{s=0;next} !s{print}' "$f")
cur=$(awk -v b="$B" -v e="$E" '$0==b{s=1;next} $0==e{s=0;next} s{print}' "$f")
case "$NC_HBA_ACTION" in
add) lines=$(printf '%s\n%s\n' "$cur" "$NC_HBA_LINE" | awk 'NF && !seen[$0]++') ;;
remove) lines=$(printf '%s\n' "$cur" | awk -v l="$NC_HBA_LINE" 'NF && $0!=l') ;;
*) echo "unknown action $NC_HBA_ACTION" >&2; exit 2 ;;
esac
tmp="$f.nebula-new"
{
  printf '%s\n' "$rest"
  if [ -n "$lines" ]; then printf '%s\n%s\n%s\n' "$B" "$lines" "$E"; fi
} > "$tmp"
mv "$tmp" "$f"
`

// HBAPlan builds the command that adds (add) or removes the pg_hba.conf line of
// role. Run it before the SQL step so that step's pg_reload_conf applies it.
func HBAPlan(s Spec, add bool, role string) (ExecPlan, error) {
	if s.Engine != Postgres {
		return ExecPlan{}, ErrNotPostgres
	}
	if !ValidIdent(role) {
		return ExecPlan{}, errors.New("invalid replication role")
	}
	action := "remove"
	if add {
		action = "add"
	}
	return ExecPlan{
		Argv: []string{"sh", "-c", hbaScript},
		Env:  []string{"NC_HBA_ACTION=" + action, "NC_HBA_LINE=" + HBALine(role)},
		User: "postgres",
	}, nil
}

// replConfRender writes nebula.replica.conf (shared by the clone and the
// reconfigure scripts). The password is not in it: the container environment
// carries PGPASSWORD at runtime.
const replConfRender = `printf "primary_conninfo = 'host=%s port=%s user=%s application_name=%s'\nprimary_slot_name = '%s'\n" "$NC_HOST" "$NC_PORT" "$NC_USER" "$NC_SLOT" "$NC_SLOT"`

// cloneScript is the db_replica_clone container's script. It runs
// as root in a one-off container of the replica's image with the replica's data
// volume mounted; PGPASSWORD, NC_HOST, NC_PORT, NC_USER and NC_SLOT come from the
// environment. A slot left by an earlier failed attempt is dropped first (it only
// matters for this replica's slot name, and the data directory is empty).
const cloneScript = `set -eu
[ ! -s "$PGDATA/PG_VERSION" ] || { echo "data directory is not empty" >&2; exit 4; }
install -d -o postgres -g postgres -m 0700 "$PGDATA"
PGCONNECT_TIMEOUT=15 psql "host=$NC_HOST port=$NC_PORT user=$NC_USER dbname=postgres replication=true" -X -q -c "DROP_REPLICATION_SLOT $NC_SLOT" >/dev/null 2>&1 || true
gosu postgres pg_basebackup -h "$NC_HOST" -p "$NC_PORT" -U "$NC_USER" -D "$PGDATA" -X stream -C -S "$NC_SLOT" --checkpoint=fast --no-password -v
: > "$PGDATA/standby.signal"
` + replConfRender + ` > "$PGDATA/nebula.replica.conf"
grep -q nebula.replica.conf "$PGDATA/postgresql.conf" || echo "include_if_exists = 'nebula.replica.conf'" >> "$PGDATA/postgresql.conf"
chown postgres:postgres "$PGDATA/standby.signal" "$PGDATA/nebula.replica.conf" "$PGDATA/postgresql.conf"
chmod 0600 "$PGDATA/nebula.replica.conf"
`

// CloneScript returns the constant script of db_replica_clone.
func CloneScript() string { return cloneScript }

// CloneEnv builds the environment of the clone container (password included).
func CloneEnv(host string, port int, user, slot, password string) ([]string, error) {
	if !ValidReplHost(host) || port < 1 || port > 65535 || !ValidIdent(user) || !ValidIdent(slot) {
		return nil, errors.New("invalid clone target")
	}
	if password == "" {
		return nil, errors.New("replication password is required")
	}
	return []string{
		"NC_HOST=" + host, "NC_PORT=" + strconv.Itoa(port), "NC_USER=" + user, "NC_SLOT=" + slot,
		"PGPASSWORD=" + password,
	}, nil
}

// reconfigureScript rewrites nebula.replica.conf of a running replica.
const reconfigureScript = `set -eu
umask 077
[ -e "$PGDATA/standby.signal" ] || { echo "not a replica: no standby.signal in $PGDATA" >&2; exit 5; }
f="$PGDATA/nebula.replica.conf"
` + replConfRender + ` > "$f.new"
mv "$f.new" "$f"
`

// ReconfigurePlan builds the command that re-renders nebula.replica.conf for the
// new host, port, user and slot. Follow it with ReplPlan(repl_reconfigure), whose
// pg_reload_conf makes the walreceiver reconnect.
func ReconfigurePlan(s Spec, a ReplArgs) (ExecPlan, error) {
	if s.Engine != Postgres {
		return ExecPlan{}, ErrNotPostgres
	}
	if !ValidReplHost(a.Host) || a.Port < 1 || a.Port > 65535 || !ValidIdent(a.Role) || !ValidIdent(a.Slot) {
		return ExecPlan{}, errors.New("invalid replica upstream")
	}
	return ExecPlan{
		Argv: []string{"sh", "-c", reconfigureScript},
		Env:  []string{"NC_HOST=" + a.Host, "NC_PORT=" + strconv.Itoa(a.Port), "NC_USER=" + a.Role, "NC_SLOT=" + a.Slot},
		User: "postgres",
	}, nil
}

// promoteCleanupScript empties the replica include file after a promote, so a
// restart never points a primary at its old upstream.
const promoteCleanupScript = `set -eu
: > "$PGDATA/nebula.replica.conf"
rm -f "$PGDATA/standby.signal"
`

// PromoteCleanupPlan builds the command that clears the replica configuration
// of a promoted server.
func PromoteCleanupPlan(s Spec) (ExecPlan, error) {
	if s.Engine != Postgres {
		return ExecPlan{}, ErrNotPostgres
	}
	return ExecPlan{Argv: []string{"sh", "-c", promoteCleanupScript}, User: "postgres"}, nil
}

// replicaStartScript is the replica container's command. The stock entrypoint
// would run initdb on an empty data directory and start a healthy-looking,
// non-replicating primary; this refuses instead. The replication password
// reaches the walreceiver through PGPASSWORD (libpq's environment default).
const replicaStartScript = `[ -s "$PGDATA/PG_VERSION" ] || { echo "nebulactrl: replica data directory is empty; rebuild the replica" >&2; exit 3; }; export PGPASSWORD="$POSTGRES_REPLICATION_PASSWORD"; exec docker-entrypoint.sh postgres`

// ReplicaStartCommand is the container command of a PostgreSQL replica.
func ReplicaStartCommand() []string { return []string{"sh", "-c", replicaStartScript} }

// ControlDataScript is the argv script of the repl_final_lsn one-off container:
// pg_controldata on the stopped primary's data directory.
const ControlDataScript = `exec pg_controldata "$PGDATA"`

// ControlData is what ParseControlData reads from pg_controldata.
type ControlData struct {
	// State is "Database cluster state", e.g. "shut down" or "in production".
	State string
	// CheckpointLSN is "Latest checkpoint location".
	CheckpointLSN string
}

// CleanShutdown reports whether the cluster was shut down cleanly, i.e. whether
// the checkpoint location is its final WAL position.
func (c ControlData) CleanShutdown() bool { return c.State == "shut down" }

// ParseControlData parses pg_controldata output.
func ParseControlData(out string) (ControlData, error) {
	var c ControlData
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Database cluster state":
			c.State = strings.TrimSpace(v)
		case "Latest checkpoint location":
			c.CheckpointLSN = strings.TrimSpace(v)
		}
	}
	if c.State == "" || !ValidLSN(c.CheckpointLSN) {
		return ControlData{}, errors.New("pg_controldata output has no cluster state or checkpoint location")
	}
	return c, nil
}

// LSNMarker prefixes the "nebula:lsn:<position>" line of the freeze and promote SQL.
const LSNMarker = "nebula:lsn:"

// ParseLSN returns the position printed by ReplFreezeSQL or ReplPromoteSQL.
func ParseLSN(stdout string) (string, error) {
	lsn := ""
	for _, line := range strings.Split(stdout, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), LSNMarker); ok {
			lsn = v
		}
	}
	if !ValidLSN(lsn) {
		return "", errors.New("no WAL position in the command output")
	}
	return lsn, nil
}

// replStatusJSON mirrors the JSON of ReplStatusSQL.
type replStatusJSON struct {
	Role           string  `json:"role"`
	InRecovery     bool    `json:"inRecovery"`
	ReceiverStatus string  `json:"receiverStatus"`
	ReceiveLSN     *string `json:"receiveLsn"`
	ReplayLSN      *string `json:"replayLsn"`
	ReplayLagBytes int64   `json:"replayLagBytes"`
	Slots          []struct {
		Name        string  `json:"name"`
		Active      bool    `json:"active"`
		WALStatus   *string `json:"walStatus"`
		SafeWALSize *int64  `json:"safeWalSize"`
	} `json:"slots"`
	Standbys []struct {
		ApplicationName *string `json:"applicationName"`
		State           *string `json:"state"`
		SyncState       *string `json:"syncState"`
		LagBytes        int64   `json:"lagBytes"`
		ReplayLagMs     *int64  `json:"replayLagMs"`
	} `json:"standbys"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ParseReplStatus parses the output of ReplStatusSQL: the last JSON line. The
// caller sets CheckedAt.
func ParseReplStatus(stdout string) (agentprotocol.ReplicationStatus, error) {
	line := ""
	for _, l := range strings.Split(stdout, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "{") {
			line = l
		}
	}
	if line == "" {
		return agentprotocol.ReplicationStatus{}, errors.New("no replication status in the command output")
	}
	var j replStatusJSON
	if err := json.Unmarshal([]byte(line), &j); err != nil {
		return agentprotocol.ReplicationStatus{}, fmt.Errorf("parse replication status: %w", err)
	}
	role := agentprotocol.ReplRolePrimary
	if j.Role == "replica" {
		role = agentprotocol.ReplRoleReplica
	}
	st := agentprotocol.ReplicationStatus{
		Role: role, InRecovery: j.InRecovery, ReceiverStatus: j.ReceiverStatus,
		ReceiveLSN: deref(j.ReceiveLSN), ReplayLSN: deref(j.ReplayLSN), ReplayLagBytes: j.ReplayLagBytes,
	}
	for _, s := range j.Slots {
		st.Slots = append(st.Slots, agentprotocol.ReplicationSlot{
			Name: s.Name, Active: s.Active, WALStatus: deref(s.WALStatus), SafeWALSize: s.SafeWALSize,
		})
	}
	for _, s := range j.Standbys {
		st.Standbys = append(st.Standbys, agentprotocol.ReplicationStandby{
			ApplicationName: deref(s.ApplicationName), State: deref(s.State), SyncState: deref(s.SyncState),
			LagBytes: s.LagBytes, ReplayLagMs: s.ReplayLagMs,
		})
	}
	return st, nil
}
