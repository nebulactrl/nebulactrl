package registryadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/registries"
)

// manifestAccept lists every manifest media type, so the registry answers with
// the manifest as stored (an index stays an index).
const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// maxRepositories bounds how many repositories one purge walks.
const maxRepositories = 10000

// Purge deletes every manifest under the team's repository prefix through the
// registry's API. The blobs they referenced are reclaimed by the next
// garbage-collection window. It is idempotent.
func (b *Backend) Purge(ctx context.Context, s instsvc.Slice) error {
	prefix := strings.TrimSuffix(s.Ref, "/") + "/"
	if prefix == "/" {
		return errors.New("a registry slice needs its repository prefix")
	}
	repos, err := b.repositories(ctx, prefix)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if err := b.purgeRepository(ctx, repo); err != nil {
			return fmt.Errorf("purge %s: %w", repo, err)
		}
	}
	return nil
}

// do sends one request to the registry with a token for access.
func (b *Backend) do(ctx context.Context, method, path string, header map[string]string, access ...registries.RepoAccess) (*http.Response, error) {
	tok, err := b.SignRepoToken(ctx, access...)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, b.cfg.URL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return b.cfg.HTTP.Do(req)
}

// repositories lists the repositories whose name starts with prefix.
func (b *Backend) repositories(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	last := ""
	for {
		q := url.Values{"n": {"1000"}}
		if last != "" {
			q.Set("last", last)
		}
		resp, err := b.do(ctx, http.MethodGet, "/v2/_catalog?"+q.Encode(), nil,
			registries.RepoAccess{Type: "registry", Name: "catalog", Actions: []string{"*"}})
		if err != nil {
			return nil, err
		}
		var page struct {
			Repositories []string `json:"repositories"`
		}
		err = decode(resp, &page)
		if err != nil {
			return nil, fmt.Errorf("list repositories: %w", err)
		}
		for _, r := range page.Repositories {
			if strings.HasPrefix(r, prefix) {
				out = append(out, r)
			}
		}
		if len(page.Repositories) < 1000 || len(out) > maxRepositories {
			return out, nil
		}
		last = page.Repositories[len(page.Repositories)-1]
	}
}

func decode(resp *http.Response, v any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the registry answered %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

func (b *Backend) purgeRepository(ctx context.Context, repo string) error {
	resp, err := b.do(ctx, http.MethodGet, "/v2/"+repo+"/tags/list", nil,
		registries.RepoAccess{Name: repo, Actions: []string{registries.ActionPull}})
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil
	}
	var tags struct {
		Tags []string `json:"tags"`
	}
	if err := decode(resp, &tags); err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	for _, tag := range tags.Tags {
		if err := b.deleteTag(ctx, repo, tag); err != nil {
			return fmt.Errorf("tag %s: %w", tag, err)
		}
	}
	return nil
}

// deleteTag deletes the manifest a tag points to, and the manifests an index
// holds (garbage collection keeps blobs they reference).
func (b *Backend) deleteTag(ctx context.Context, repo, tag string) error {
	acc := registries.RepoAccess{Name: repo, Actions: []string{registries.ActionPull, registries.ActionDelete}}
	resp, err := b.do(ctx, http.MethodGet, "/v2/"+repo+"/manifests/"+url.PathEscape(tag), map[string]string{"Accept": manifestAccept}, acc)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	var m struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := decode(resp, &m); err != nil {
		return err
	}
	for _, c := range m.Manifests {
		if err := b.deleteManifest(ctx, repo, c.Digest, acc); err != nil {
			return err
		}
	}
	if digest == "" {
		return errors.New("the registry did not name the manifest's digest")
	}
	return b.deleteManifest(ctx, repo, digest, acc)
}

func (b *Backend) deleteManifest(ctx context.Context, repo, digest string, acc registries.RepoAccess) error {
	resp, err := b.do(ctx, http.MethodDelete, "/v2/"+repo+"/manifests/"+url.PathEscape(digest), nil, acc)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return fmt.Errorf("delete %s: the registry answered %d", digest, resp.StatusCode)
}
