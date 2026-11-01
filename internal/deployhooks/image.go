package deployhooks

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Registry hosts the hook understands by name.
const (
	hostDockerHub = "docker.io"
	hostGHCR      = "ghcr.io"
)

var (
	tagRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	digestRe = regexp.MustCompile(`^(sha256:[a-f0-9]{64}|sha512:[a-f0-9]{128})$`)
	// pathRe is a repository path: lowercase components joined by "/".
	pathRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
)

// imageRef is an image reference split into the parts that matter for
// comparing repositories.
type imageRef struct {
	// Host is the registry ("docker.io" for Docker Hub, however it is spelled).
	Host string
	// Path is the lowercase repository path ("library/nginx", "acme/api").
	Path string
	// Name is the reference as the user wrote it, without tag or digest: it is
	// the base an override tag or digest is appended to.
	Name   string
	Tag    string
	Digest string
}

// Repo is the registry-qualified repository, e.g. "ghcr.io/acme/api".
func (r imageRef) Repo() string { return r.Host + "/" + r.Path }

var errBadRef = errors.New("not a valid image reference")

// parseImage splits repo[:tag][@digest] and normalises the Docker Hub
// spellings (nginx, library/nginx, docker.io/library/nginx and
// index.docker.io/library/nginx are one repository).
func parseImage(ref string) (imageRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.ContainsAny(ref, " \t\r\n") {
		return imageRef{}, errBadRef
	}
	var out imageRef
	rest := ref
	if i := strings.Index(rest, "@"); i >= 0 {
		out.Digest, rest = rest[i+1:], rest[:i]
		if !digestRe.MatchString(out.Digest) {
			return imageRef{}, fmt.Errorf("%w: bad digest", errBadRef)
		}
	}
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		out.Tag, rest = rest[i+1:], rest[:i]
		if !tagRe.MatchString(out.Tag) {
			return imageRef{}, fmt.Errorf("%w: bad tag", errBadRef)
		}
	}
	out.Name = rest
	host, path := "", rest
	if i := strings.Index(rest, "/"); i >= 0 {
		first := rest[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host, path = strings.ToLower(first), rest[i+1:]
		}
	}
	switch host {
	case "", "docker.io", "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		host = hostDockerHub
		if !strings.Contains(path, "/") {
			path = "library/" + path
		}
	}
	path = strings.ToLower(path)
	if !pathRe.MatchString(path) {
		return imageRef{}, fmt.Errorf("%w: bad repository name", errBadRef)
	}
	out.Host, out.Path = host, path
	return out, nil
}

// sameRepo reports whether two references name one repository.
func sameRepo(a, b imageRef) bool { return a.Host == b.Host && a.Path == b.Path }

// effectiveTag is the tag a pull of r resolves: "latest" when none is given,
// "" when the reference is pinned by digest (a pinned image has no moving tag
// for a registry push to match).
func (r imageRef) effectiveTag() string {
	if r.Digest != "" {
		return ""
	}
	if r.Tag == "" {
		return "latest"
	}
	return r.Tag
}

// overrideError is a problem with the requested reference (HTTP 422).
type overrideError struct{ code, msg string }

func (e *overrideError) Error() string { return e.msg }

// resolveOverride turns the ?tag= and ?digest= parameters into the image
// reference to deploy. The reference always stays in the service image's
// repository: a bare tag or digest is appended to it, a full reference is
// accepted only when it names the same repository. Both empty returns "".
func resolveOverride(serviceImage, tag, digest string) (string, *overrideError) {
	if tag == "" && digest == "" {
		return "", nil
	}
	if tag != "" && digest != "" {
		return "", &overrideError{"validation_failed", "give tag or digest, not both"}
	}
	svc, err := parseImage(serviceImage)
	if err != nil {
		return "", &overrideError{"validation_failed", "the service image is not a plain image reference, so a tag or digest cannot be applied to it"}
	}
	param, what := tag, "tag"
	if digest != "" {
		param, what = digest, "digest"
	}
	bare := !strings.ContainsAny(param, "/:@")
	if what == "digest" {
		bare = !strings.Contains(param, "@") // sha256:… itself holds a colon
	}
	if bare {
		switch {
		case what == "tag" && tagRe.MatchString(param):
			return svc.Name + ":" + param, nil
		case what == "digest" && digestRe.MatchString(param):
			return svc.Name + "@" + param, nil
		}
		return "", &overrideError{"validation_failed", fmt.Sprintf("%s %q is not valid", what, param)}
	}
	// A full reference (repo:tag, repo@sha256:…).
	full, err := parseImage(param)
	if err != nil {
		return "", &overrideError{"validation_failed", fmt.Sprintf("%s %q is not a valid image reference", what, param)}
	}
	if !sameRepo(svc, full) {
		return "", &overrideError{"image_outside_repository", fmt.Sprintf("%s %q is in %s; the hook only deploys images of %s", what, param, full.Repo(), svc.Repo())}
	}
	if full.Tag == "" && full.Digest == "" {
		return "", &overrideError{"validation_failed", fmt.Sprintf("%s %q has neither a tag nor a digest", what, param)}
	}
	if what == "digest" && full.Digest == "" {
		return "", &overrideError{"validation_failed", fmt.Sprintf("digest %q has no digest", param)}
	}
	return param, nil
}
