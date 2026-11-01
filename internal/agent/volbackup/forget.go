package volbackup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Forget removes snapshots: the explicit ids and/or the victims of the
// retention policy (minus the protected ids), then prunes the repository.
// Retention is decided by dry run so that protected (locked) snapshots survive
// without being re-tagged: restic gives a snapshot a new id when its tags change.
func (r *Runner) Forget(ctx context.Context, p agentprotocol.VolumeForgetPayload, secrets map[string]string, logf func(string)) (agentprotocol.VolumeForgetResult, error) {
	if logf == nil {
		logf = nopLog
	}
	res := agentprotocol.VolumeForgetResult{Outcome: agentprotocol.OutcomeFailed}
	fail := func(format string, a ...any) (agentprotocol.VolumeForgetResult, error) {
		res.Message = scrub(fmt.Sprintf(format, a...), secrets)
		return res, errors.New(res.Message)
	}
	if p.ServiceID == "" {
		return fail("invalid volume_forget payload: serviceId is required")
	}
	spec, mounts, network, err := r.destination(ctx, p.Repo)
	if err != nil {
		return fail("%v", err)
	}
	op := randomID()
	defer r.track(op)()
	unlockRepo, err := r.lockRepo(ctx, p.ServiceID)
	if err != nil {
		return fail("waiting for another backup of this service: %v", err)
	}
	defer unlockRepo()

	h, err := r.newHelper(ctx, helperOpts{
		op: op, kind: "f", serviceID: p.ServiceID, mounts: mounts, network: network, repo: spec, secrets: secrets,
	}, logf)
	if err != nil {
		return fail("%v", err)
	}
	defer h.close()
	exists, err := h.openRepo(ctx, false)
	if err != nil {
		return fail("%v", err)
	}
	if !exists {
		res.Outcome, res.Message = agentprotocol.OutcomeSucceeded, "the repository does not exist yet; nothing to remove"
		return res, nil
	}
	before, err := h.listSnapshotIDs(ctx)
	if err != nil {
		return fail("%v", err)
	}

	var victims []string
	for _, id := range p.SnapshotIDs {
		for _, full := range before {
			if id != "" && strings.HasPrefix(full, id) && !slices.Contains(victims, full) {
				victims = append(victims, full)
			}
		}
	}
	if p.Keep != nil && p.Keep.Any() {
		var out bytes.Buffer
		if err := h.resticOK(ctx, &out, forgetPolicyArgs(p.ServiceID, *p.Keep)...); err != nil {
			return fail("%v", err)
		}
		pol, err := forgetVictims(out.Bytes(), p.Protect)
		if err != nil {
			return fail("%v", err)
		}
		for _, id := range pol {
			if !slices.Contains(victims, id) {
				victims = append(victims, id)
			}
		}
	}
	if len(victims) == 0 {
		res.Outcome, res.Message = agentprotocol.OutcomeSucceeded, "nothing to remove"
		return res, nil
	}
	logf(fmt.Sprintf("removing %d snapshot(s) and pruning", len(victims)))
	if err := h.resticOK(ctx, nil, forgetIDsArgs(victims)...); err != nil {
		return fail("%v", err)
	}
	after, err := h.listSnapshotIDs(ctx)
	if err != nil {
		return fail("%v", err)
	}
	for _, id := range before {
		if !slices.Contains(after, id) {
			res.Removed = append(res.Removed, id)
		}
	}
	res.Outcome = agentprotocol.OutcomeSucceeded
	res.Message = fmt.Sprintf("removed %d snapshot(s)", len(res.Removed))
	return res, nil
}

// listSnapshotIDs lists the repository's snapshots (one repository per service).
func (h *helper) listSnapshotIDs(ctx context.Context) ([]string, error) {
	var out bytes.Buffer
	if err := h.resticOK(ctx, &out, "snapshots", "--json"); err != nil {
		return nil, err
	}
	return snapshotIDs(out.Bytes())
}
