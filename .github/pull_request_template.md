## What and why

<!-- What changes for a user or operator, and why. Link the issue: "Fixes #123". -->

## How it was checked

<!-- The tests you added or ran, and what you tried by hand. -->

- [ ] The title is a [Conventional Commit](https://www.conventionalcommits.org/) (`fix(backups): …`, `feat: …`); it becomes the squash commit and decides the version.
- [ ] `make lint test` passes; `make test-smoke` too when the agent, deploy engine, reconciler or proxy changed; `make test-e2e` when the web app's flows changed.
- [ ] A bug fix has a test that fails without the fix.
- [ ] Docs under `docs/` describe the new behaviour, in the words the UI uses.
- [ ] `make generate` was run after changing queries, handlers or CLI commands (CI's `generated` check fails otherwise).
