package agentprotocol

import (
	"fmt"
	"strconv"
	"strings"
)

// Volume ownership. A new named volume is created by Docker as root:root 0755,
// and Docker copies the ownership of the image's directory into it only when
// the mount path exists in the image. An image whose process runs as a
// non-root user and mounts a volume at a path the image does not contain
// therefore cannot write to it. A volume's Owner ("uid" or "uid:gid") makes
// the agent chown the volume's root directory (never recursively) before the
// container starts.

// MaxVolumeID is the largest uid or gid of a volume owner (the largest
// positive 32-bit value, which every Linux id fits in).
const MaxVolumeID = 2147483647

// VolumeOwner is a parsed volume owner.
type VolumeOwner struct {
	UID int
	GID int
	// HasGID is false for the "uid" form: only the owner changes and the
	// group of the volume root is left as it is.
	HasGID bool
}

// String is the canonical text of the owner: "uid" or "uid:gid".
func (o VolumeOwner) String() string {
	if !o.HasGID {
		return strconv.Itoa(o.UID)
	}
	return strconv.Itoa(o.UID) + ":" + strconv.Itoa(o.GID)
}

// ParseVolumeOwner parses "uid" or "uid:gid": decimal digits only, each in
// 0-2147483647. Names, signs, spaces and a trailing colon are refused: the
// agent chowns inside a helper container, which does not know the image's
// /etc/passwd, so only numeric ids mean the same thing everywhere.
func ParseVolumeOwner(s string) (VolumeOwner, error) {
	uidText, gidText, hasGID := strings.Cut(s, ":")
	uid, err := parseVolumeID(uidText)
	if err != nil {
		return VolumeOwner{}, fmt.Errorf("owner %q: the uid %w", s, err)
	}
	o := VolumeOwner{UID: uid, HasGID: hasGID}
	if hasGID {
		if o.GID, err = parseVolumeID(gidText); err != nil {
			return VolumeOwner{}, fmt.Errorf("owner %q: the gid %w", s, err)
		}
	}
	return o, nil
}

func parseVolumeID(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("is missing; write a number, \"uid\" or \"uid:gid\"")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("must be a number (names are not supported): %q", s)
		}
	}
	if len(s) > 10 {
		return 0, fmt.Errorf("must be at most %d", MaxVolumeID)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > MaxVolumeID {
		return 0, fmt.Errorf("must be at most %d", MaxVolumeID)
	}
	return int(n), nil
}

// NormalizeVolumeOwner returns the canonical text of owner ("" stays "": no
// owner). Leading zeros are dropped, so "010:010" and "10:10" are one owner.
func NormalizeVolumeOwner(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	o, err := ParseVolumeOwner(s)
	if err != nil {
		return "", err
	}
	return o.String(), nil
}

// Differs reports whether a volume root owned by uid:gid must be chowned to
// o. With the "uid" form the group is not compared.
func (o VolumeOwner) Differs(uid, gid int) bool {
	return uid != o.UID || (o.HasGID && gid != o.GID)
}
