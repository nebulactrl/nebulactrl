package services

import (
	"context"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// ResolveProcessFilter maps the process a log or metrics request names, taken
// from the staged configuration (what the dashboard offers), onto what the
// running release calls it:
//
//   - "" selects every process: all is true.
//   - The staged default process is the release's default process, which may be
//     named otherwise (a rename is staged until the next deploy). A release that
//     runs a single process has no process labels, prefixes or metric rows to
//     filter by: the default process is then the whole service, all is true.
//   - Any other name passes through.
//
// name is what to filter by; isDefault tells that it names the default process.
func ResolveProcessFilter(staged, release Config, requested string) (name string, isDefault, all bool) {
	switch {
	case requested == "":
		return "", false, true
	case requested != staged.DefaultProcessName():
		return requested, false, false
	case !release.HasProcesses():
		return "", true, true
	}
	return release.DefaultProcessName(), true, false
}

// ResolveServiceProcessFilter is ResolveProcessFilter for a stored service: the
// release is the one the service runs now. Without a release nothing runs, so
// the staged configuration stands for it.
func ResolveServiceProcessFilter(ctx context.Context, q *db.Queries, sv db.Service, requested string) (name string, isDefault, all bool, err error) {
	staged := DecodeConfig(sv.Config)
	release := staged
	if requested != "" {
		rel, ok, err := AppliedRelease(ctx, q, sv)
		if err != nil {
			return "", false, false, err
		}
		if ok {
			release = DecodeSnapshot(rel.Config).Config
		}
	}
	name, isDefault, all = ResolveProcessFilter(staged, release, requested)
	return name, isDefault, all, nil
}
