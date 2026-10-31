package configsync

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// fieldView is one value of one field as plans show and compare it: a
// fingerprint (never the value of a secret), a display text and whether the
// display is masked.
type fieldView struct {
	FP      string
	Display string
	Masked  bool
	// Proj names the parts of a composite value the file declares (comma
	// separated); only those parts are compared. It is stored with the owned
	// fingerprint so drift can be recomputed without the file.
	Proj string
}

// refError is a problem with a name the file uses (unknown or ambiguous), or
// with a value the platform refuses; it becomes a diagnostic at the field's line.
type refError struct {
	Code string
	Msg  string
}

func (e *refError) Error() string { return e.Msg }

func refErr(code, format string, a ...any) *refError {
	return &refError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// planCtx is what field handlers read while a plan is built.
type planCtx struct {
	ctx  context.Context
	q    *db.Queries
	box  *secretbox.Box
	live *liveEnv
	res  *projectconfig.Resolved
	rs   *projectconfig.ResolvedService // the service whose fields are being compared
	// createdID resolves the id of a service this apply already created (nil while planning).
	createdID func(slug string) (uuid.UUID, bool)
	// cloneURL reads the clone URL of a repository on a git connection (nil: not available).
	cloneURL func(ctx context.Context, connectionID uuid.UUID, repo string) (string, string, error)
	env      services.EnvScope
}

// refs returns the workspace's named things.
func (pc *planCtx) refs() *refs { return pc.live.Refs }

// fieldOps reads the live value of a field family and normalises the file's
// desired value so both can be compared by fingerprint.
type fieldOps interface {
	// live returns the live view of field id on ls; present is false when the
	// member does not exist (a variable never written, a domain not attached).
	// proj is the projection the desired view reported (see fieldView.Proj): the
	// declared parts of a value that the file owns.
	live(pc *planCtx, ls *liveService, id, proj string) (v fieldView, present bool)
	// desired returns the view of the file's value. ls is nil when the service
	// is created by this apply. Unowned parts of a value (a domain's undeclared
	// keys, the comment of a string-form variable) are compared as they are live.
	desired(pc *planCtx, ls *liveService, fv projectconfig.FieldValue) (fieldView, error)
}

// opsFor returns the handler of a field id.
func opsFor(id string) fieldOps {
	switch {
	case strings.HasPrefix(id, "config."):
		return configOps{}
	case strings.HasPrefix(id, "variables."):
		return varOps{}
	case strings.HasPrefix(id, "domains."):
		if strings.HasSuffix(id, ".cloudflare") {
			return cloudflareOps{}
		}
		return domainOps{}
	case strings.HasPrefix(id, "volumes."):
		return volumeOps{}
	case strings.HasPrefix(id, "networks."):
		return networkOps{}
	case strings.HasPrefix(id, "links."):
		return linkOps{}
	case id == "name" || id == "server" || id == "generate_domain":
		return metaOps{}
	case strings.HasPrefix(id, "scale."):
		return scaleOps{}
	case strings.HasPrefix(id, "processes.") && strings.Contains(id, ".scale."):
		return processScaleOps{}
	case strings.HasPrefix(id, "git."):
		return gitOps{}
	case strings.HasPrefix(id, "uptime."):
		return uptimeOps{}
	case strings.HasPrefix(id, "tasks."):
		return taskOps{}
	case strings.HasPrefix(id, "backups."):
		return backupOps{}
	}
	return nil
}

// view builds a fieldView from a comparable value and its display.
func view(v any, display string) fieldView {
	return fieldView{FP: projectconfig.Fingerprint(v), Display: display}
}

// maskedView is a view whose display is hidden.
func maskedView(v any, display string) fieldView {
	return fieldView{FP: projectconfig.Fingerprint(v), Display: display, Masked: true}
}
