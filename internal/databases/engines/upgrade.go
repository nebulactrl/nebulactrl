package engines

import "fmt"

// Upgrade methods.
const (
	// MethodNone means nothing can be done (same version or blocked).
	MethodNone = "none"
	// MethodRedeploy changes the image tag in place and redeploys: the engine
	// upgrades its own on-disk format at start.
	MethodRedeploy = "redeploy"
	// MethodPGUpgrade copies the data directory through pg_upgrade
	// (tianon/postgres-upgrade) into a fresh volume.
	MethodPGUpgrade = "pg_upgrade"
	// MethodDumpRestore restores a backup into a new database at the target
	// version and lets the user swap references.
	MethodDumpRestore = "dump_restore"
)

// PGUpgradeImage is the pg_upgrade helper image for a pair of majors.
func PGUpgradeImage(from, to string) string {
	return fmt.Sprintf("tianon/postgres-upgrade:%d-to-%d", Major(from), Major(to))
}

// UpgradePlan is what changing a database's version involves.
type UpgradePlan struct {
	Engine      string   `json:"engine"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Kind        string   `json:"kind"` // same | upgrade | downgrade | invalid
	Method      string   `json:"method"`
	Image       string   `json:"image,omitempty"` // target image
	HelperImage string   `json:"helperImage,omitempty"`
	Steps       []string `json:"steps"`
	Warnings    []string `json:"warnings"`
	Blockers    []string `json:"blockers"`
	Downtime    string   `json:"downtime"`
	// Executable is true when the control plane can run the method (redeploy
	// and pg_upgrade); dump_restore is a guided manual flow.
	Executable bool `json:"executable"`
}

// PlanUpgrade plans moving the database described by from to toVersion.
func PlanUpgrade(from Spec, toVersion string) UpgradePlan {
	from = from.Normalize()
	p := UpgradePlan{Engine: from.Engine, From: from.Version, To: toVersion, Method: MethodNone,
		Steps: []string{}, Warnings: []string{}, Blockers: []string{}, Downtime: "none"}
	e := Lookup(from.Engine)
	if e == nil {
		p.Kind = "invalid"
		p.Blockers = append(p.Blockers, "Unknown database engine.")
		return p
	}
	to := from
	to.Version = toVersion
	if err := to.Validate(); err != nil {
		p.Kind = "invalid"
		p.Blockers = append(p.Blockers, err.Error())
		return p
	}
	p.Image = to.Image()
	switch c := CompareVersions(toVersion, from.Version); {
	case c == 0:
		p.Kind = "same"
		p.Method = MethodRedeploy
		p.Executable = true
		p.Downtime = "a short outage while the database restarts"
		p.Steps = append(p.Steps, "Redeploy the database to pick up the latest patch release of "+e.Label+" "+from.Version+".")
		return p
	case c < 0:
		p.Kind = "downgrade"
		p.Blockers = append(p.Blockers,
			"Downgrades are not supported: an older server may refuse a newer on-disk format. Restore a backup into a new database at "+toVersion+" instead.")
		return p
	}
	p.Kind = "upgrade"
	p.Warnings = append(p.Warnings, "Take a backup first. Upgrades cannot be undone in place.")

	switch from.Engine {
	case Postgres:
		if from.Flavor != "" {
			p.Method = MethodDumpRestore
			p.Downtime = "none for the original; the new database is built beside it"
			p.Steps = append(p.Steps,
				"Back up the database.",
				fmt.Sprintf("Restore the backup into a new %s %s database (Backups → Restore to a new database).", e.Label, toVersion),
				"Point your services at the new database with ${{new.DATABASE_URL}}, verify, then delete the old database.")
			p.Warnings = append(p.Warnings, "PostGIS libraries must match the data, so pg_upgrade is not offered for the PostGIS flavor.")
			return p
		}
		p.Method = MethodPGUpgrade
		p.Executable = true
		p.HelperImage = PGUpgradeImage(from.Version, toVersion)
		p.Downtime = "the database is stopped while its data is converted (minutes for typical databases)"
		p.Steps = append(p.Steps,
			"The database is stopped.",
			fmt.Sprintf("pg_upgrade (%s) converts the data into a new volume; the old volume is kept untouched.", p.HelperImage),
			"The database is switched to the new volume and the new image as a staged change.",
			"Deploy the staged change to start PostgreSQL "+toVersion+".",
			"Run vacuumdb --all --analyze-in-stages afterwards to rebuild planner statistics.")
		if Major(toVersion) >= 18 && Major(from.Version) < 18 {
			p.Warnings = append(p.Warnings, "PostgreSQL 18 uses a new data directory layout; the new volume is mounted one level higher.")
		}
		p.Warnings = append(p.Warnings, "The old volume stays on the server until you delete it; it can be used to roll back by restoring the previous settings.")
	case MySQL, MariaDB:
		p.Method = MethodRedeploy
		p.Executable = true
		p.Downtime = "a short outage while the database restarts and upgrades its data files"
		p.Steps = append(p.Steps, "Change the image to "+p.Image+" as a staged change.", "Deploy the staged change; the server upgrades its data files on start.")
	case Valkey, Redis:
		p.Method = MethodRedeploy
		p.Executable = true
		p.Downtime = "a short outage while the database restarts"
		p.Steps = append(p.Steps, "Change the image to "+p.Image+" as a staged change.", "Deploy the staged change; the dataset is loaded by the new version.")
		p.Warnings = append(p.Warnings, "Newer RDB files cannot be read by older versions, so this cannot be reverted without a backup.")
	case ClickHouse:
		p.Method = MethodRedeploy
		p.Executable = true
		p.Downtime = "a short outage while the database restarts"
		p.Steps = append(p.Steps, "Change the image to "+p.Image+" as a staged change.", "Deploy the staged change.")
	case MongoDB:
		p.Method = MethodDumpRestore
		p.Downtime = "none for the original; the new database is built beside it"
		p.Steps = append(p.Steps,
			"Back up the database.",
			fmt.Sprintf("Restore the backup into a new %s %s database.", e.Label, toVersion),
			"Point your services at the new database, verify, then delete the old database.")
		p.Warnings = append(p.Warnings, "MongoDB major upgrades need featureCompatibilityVersion steps, so they are done through a new database.")
	}
	return p
}

// PGUpgradeLayout describes how the old and new volumes are mounted into the
// pg_upgrade helper container and where the data directories are inside them.
type PGUpgradeLayout struct {
	OldMount string // where the old volume is mounted
	NewMount string // where the new volume is mounted
	OldData  string // PGDATAOLD
	NewData  string // PGDATANEW
}

// PGUpgradeLayoutFor returns the helper layout for upgrading from→to. The
// volume of PostgreSQL ≤17 holds the data directory itself; from 18 on it
// holds <major>/docker below the mount point.
func PGUpgradeLayoutFor(from, to string) PGUpgradeLayout {
	l := PGUpgradeLayout{OldMount: "/var/lib/postgresql/old", NewMount: "/var/lib/postgresql/new"}
	l.OldData = l.OldMount
	if Major(from) >= 18 {
		l.OldData = fmt.Sprintf("%s/%d/docker", l.OldMount, Major(from))
	}
	l.NewData = l.NewMount
	if Major(to) >= 18 {
		l.NewData = fmt.Sprintf("%s/%d/docker", l.NewMount, Major(to))
	}
	return l
}
