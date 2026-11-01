# Railpack fixtures

Two repositories without a Dockerfile, used by `tests/integration/railpack_test.go`
(`TestRailpackBuilds`): the test turns each directory into a git repository,
serves them with `git daemon` and deploys them through the control plane, so
they are built by the pinned Railpack.

- `node-app`: a dependency-free Node HTTP server; the body is `railpack-node-ok`.
- `static-site`: an `index.html` served by Caddy (Railpack's static provider); the
  body contains `railpack-static-ok`.
