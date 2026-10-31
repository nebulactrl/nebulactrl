# template-translate

One-time, offline helper that drafts NebulaCtrl templates from the Coolify
(`templates/compose/*.yaml`, Apache-2.0) and Dokploy
(`blueprints/<id>/{docker-compose.yml,template.toml,meta.json}`, MIT)
catalogs. It is never used at runtime and never writes to `templates/catalog`:
drafts go to `templates/drafts/<id>.yaml`, unsupported templates are listed in
`templates/drafts/SKIPPED.txt`. `templates/drafts` is the tool's local working
directory and is not committed, so `SKIPPED.txt` and `REJECTED.txt` live there
only on the machine that ran the tool. Format and curation rules:
`docs/operations/templates.md`. Attribution: `templates/NOTICE`.

Drafts are deliberately unfinished: the digest is the all-zero placeholder,
`upstream.license` is empty, `upstream.class` is a placeholder, and every guess
carries a `# REVIEW:` comment, so a draft fails `Validate` until it is reviewed.

## Run

```
git clone --depth 1 --filter=blob:none --sparse https://github.com/coollabsio/coolify /tmp/tt/coolify
git -C /tmp/tt/coolify sparse-checkout set templates/compose
git clone --depth 1 --filter=blob:none --sparse https://github.com/Dokploy/templates /tmp/tt/dokploy
git -C /tmp/tt/dokploy sparse-checkout set blueprints

go run ./tools/template-translate -coolify /tmp/tt/coolify -dokploy /tmp/tt/dokploy -limit 90
go run ./tools/template-translate -coolify /tmp/tt/coolify -dokploy /tmp/tt/dokploy -only umami,ghost
rm -rf /tmp/tt
```

Flags: `-coolify`/`-dokploy` (checkout roots, or the `templates/compose` /
`blueprints` directories themselves), `-out` (default `templates/drafts`),
`-catalog` (ids already there are skipped, default `templates/catalog`), `-only
a,b` (explicit ids, no selection heuristics), `-limit N`.

Default selection: at most three services, each a plain `image:`; not already in
the catalog; no Coolify `ignore: true`; no Bitnami/enterprise/abandoned images.
Apps present in both catalogs come first, then Coolify order, then Dokploy-only
apps. Coolify is preferred as the source for apps in both (Dokploy metadata
fills gaps). Skipped as unsupported: `build:`, `privileged`, `network_mode`,
`devices`, `cap_add` with a capability a restricted server refuses (NET_ADMIN,
SYS_ADMIN, anything outside the safe list), `volumes_from`, `env_file`, custom
networks, more than 8 services, host bind mounts without content,
`${jwt:…}`-style helpers.

Container options map into the template's `docker:` block (`dockeropts.go`):
`entrypoint` (an empty `entrypoint: []` becomes the command's program),
`cap_add` (safe list), `cap_drop`, `security_opt: no-new-privileges`, `read_only`,
`pids_limit`, `hostname`, `dns`, `sysctls`, `user`, `group_add`, `ulimits`
(nofile, memlock), `shm_size`, `extra_hosts`, `init`, `tmpfs` and `stop_signal`.
Each option is validated alone with the restricted-server rules; one that fails
is left out and listed in the service's `dropped from the compose service` REVIEW
note. The translator never emits `requiresUnrestricted`.

## Enrich (pins, licences, rejections)

Drafts need the same mechanical lookups every time, so `-enrich` does them
without pulling anything:

```
go run ./tools/template-translate -enrich templates/drafts -enrich-out /tmp/tt/stage
```

For every draft it reads the upstream repository with `gh api` (licence SPDX id
and class, the licence file URL, archived, last push), picks a stable versioned
tag for floating or missing ones (the tag that has the same index digest as
`latest` when there is one, else the newest `X.Y.Z`; prereleases, moving majors
and date stamps are skipped), pins the multi-arch index digest with a registry
HEAD request, narrows `requires.arch` to what the images publish, rewrites the
project-run registry hosts that only redirect to a public registry
(`vanityRegistries`), and writes the result to `<out>/<id>.yaml` with the
remaining open questions in `<out>/<id>.notes.txt`. Docker Hub is asked through
its web API for platforms, so a run never touches the registry pull limit.

It **rejects** (lists in `<out>/REJECTED.auto.txt`, writes no file) a draft whose
upstream is archived or has had no push for 12 months, whose images have no
amd64 manifest, or that has no versioned tag at all. Ids in
`templates/drafts/REJECTED.txt` are skipped. Everything else still needs the
review below: the enrich step is a head start, not an approval.

```
go run ./tools/template-translate -check <file>...   # tier-1 validation + no draft markers
go run ./tools/template-translate -resolve repo:tag   # digest and platforms, HEAD only
go run ./tools/template-translate -tags repo[:tag]    # newest stable tags
```

## Review checklist

1. `grep REVIEW` the draft and resolve each note.
2. Set `upstream.license` (SPDX), `upstream.class` (`licenseUrl` for
   source-available) and check `links`, `category`, `tags`, `icon`, `name`.
3. Replace floating tags with a stable version (`-enrich` does it; confirm the
   choice) and pin the digest with `-resolve` or
   `go run ./tools/template-pin -update <file>`.
4. Check each healthcheck (guessed `tcp` probes), the domain port, and that
   env values still make sense (`${{db.DATABASE_URL}}` etc. for managed databases).
5. Published ports default to `publish: false`: set `true` for game servers.
6. Review files for secrets, and variables generated into commands/files.
7. Move the file to `templates/catalog/`, keep `origin`, bump nothing else, run
   `-check` on it and `go test ./internal/templates/`.
8. Rejected ids go to `templates/drafts/REJECTED.txt` as `id: reason`.
