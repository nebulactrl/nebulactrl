package engine

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// volumeOwnerRoot is where the owner helper mounts the volume. Only this
// directory (the volume root) is ever chowned, never its contents.
const volumeOwnerRoot = "/v"

// volumeOwnerHelperTTL is how long an owner helper idles before it exits on its
// own, should the agent die before it removes it.
const volumeOwnerHelperTTL = "120"

// volumeOwnerHelperTimeout bounds the whole check and chown of one volume.
const volumeOwnerHelperTimeout = 2 * time.Minute

// volumeOwnerOf parses the output of `stat -c %u:%g` of the volume root.
func volumeOwnerOf(out string) (uid, gid int, err error) {
	u, g, ok := strings.Cut(strings.TrimSpace(out), ":")
	if !ok {
		return 0, 0, fmt.Errorf("unexpected stat output %q", strings.TrimSpace(out))
	}
	if uid, err = strconv.Atoi(u); err != nil {
		return 0, 0, fmt.Errorf("unexpected stat output %q", strings.TrimSpace(out))
	}
	if gid, err = strconv.Atoi(g); err != nil {
		return 0, 0, fmt.Errorf("unexpected stat output %q", strings.TrimSpace(out))
	}
	return uid, gid, nil
}

// ensureVolumeOwner makes the root directory of the volume v belong to
// v.Owner. A volume without an owner is left alone. The current owner is read
// first and the root is chowned only when it differs, so a volume that already
// has the right owner costs one short helper and changes nothing. The chown is
// never recursive: files inside the volume keep their owners.
//
// Both steps run in a short-lived helper container (the pinned restic image,
// which carries BusyBox) with no network, a read-only root filesystem and
// CAP_CHOWN as its only capability. The helper carries nebulactrl.helper but
// never nebulactrl.managed, so the orphan sweep and Docker cleanup leave it to
// this function, which removes it when it is done.
func ensureVolumeOwner(ctx context.Context, eng docker.Engine, v agentprotocol.VolumeMount, logf func(string)) error {
	if v.Owner == "" {
		return nil
	}
	want, err := agentprotocol.ParseVolumeOwner(v.Owner)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, volumeOwnerHelperTimeout)
	defer cancel()

	if _, err := eng.ImageInspect(ctx, agentprotocol.ResticImage); err != nil {
		if logf != nil {
			logf("Pulling the helper image that sets volume owners")
		}
		if err := eng.ImagePull(ctx, agentprotocol.ResticImage, "", nil); err != nil {
			return fmt.Errorf("pull the helper image: %w", err)
		}
	}
	spec := docker.ContainerSpec{
		Name:       agentprotocol.VolumeOwnerHelperPrefix + v.Name,
		Image:      agentprotocol.ResticImage,
		User:       "0:0",
		Entrypoint: []string{"sleep", volumeOwnerHelperTTL},
		Labels:     map[string]string{agentprotocol.LabelHelper: agentprotocol.HelperVolumeOwner},
		Mounts:     []docker.VolumeMount{{Volume: v.Name, Target: volumeOwnerRoot}},
		// No network at all; no capability but CAP_CHOWN.
		NetworkMode:    "none",
		CapDrop:        []string{"ALL"},
		CapAdd:         []string{"CHOWN"},
		SecurityOpt:    []string{"no-new-privileges"},
		ReadonlyRootfs: true,
		MemoryBytes:    agentprotocol.HelperMemoryBytes,
		NanoCPUs:       agentprotocol.HelperNanoCPUs,
	}
	// A helper a crashed agent left behind is stale.
	if old, err := eng.ContainerInspect(ctx, spec.Name); err == nil {
		_ = eng.ContainerRemove(ctx, old.ID, true, false)
	}
	id, err := eng.ContainerCreate(ctx, spec)
	if err != nil {
		return fmt.Errorf("create the helper: %w", err)
	}
	defer func() {
		rm, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer stop()
		_ = eng.ContainerRemove(rm, id, true, false)
	}()
	if err := eng.ContainerStart(ctx, id); err != nil {
		return fmt.Errorf("start the helper: %w", err)
	}

	var out, errOut bytes.Buffer
	code, err := eng.ContainerExec(ctx, id, []string{"stat", "-c", "%u:%g", volumeOwnerRoot}, docker.ExecSpec{}, &out, &errOut)
	if err != nil {
		return fmt.Errorf("read the current owner: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("read the current owner: stat exited with status %d: %s", code, execTail(&errOut))
	}
	uid, gid, err := volumeOwnerOf(out.String())
	if err != nil {
		return fmt.Errorf("read the current owner: %w", err)
	}
	if !want.Differs(uid, gid) {
		return nil
	}
	errOut.Reset()
	code, err = eng.ContainerExec(ctx, id, []string{"chown", want.String(), volumeOwnerRoot}, docker.ExecSpec{}, nil, &errOut)
	if err != nil {
		return fmt.Errorf("chown: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("chown exited with status %d: %s", code, execTail(&errOut))
	}
	if logf != nil {
		logf(fmt.Sprintf("Set the owner of volume %s (was %d:%d) to %s", v.Name, uid, gid, want))
	}
	return nil
}

func execTail(b *bytes.Buffer) string {
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "no error output"
	}
	if len(s) > 500 {
		s = s[len(s)-500:]
	}
	return s
}
