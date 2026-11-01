---
title: Processes
description: "One service, several processes: web, queue workers and schedulers from one build, one image and one release."
section: Applications
order: 105
---

# Processes: one service, several commands

A service can run more than one long-lived command from the same build. Each command is a **process**: it runs in its own containers, with its own command, optional port, replica count, health check and logs. The processes share everything else: the source, the build, the image, the variables, the volumes and the release.

Use processes for the usual web-plus-worker apps: a web server and a queue worker, a web server and a scheduler, or all three. If two commands need different images or builds, make them two services.

## The default process and the named ones

The service's own `port`, `command`, `health_check`, `stop_grace_seconds`, `memory_mb`, `cpus` and `singleton` describe its **default process**. It is called `web` unless you set `process_name`. The other processes are added under `processes.<name>`. A service without processes behaves exactly as before.

A process name is 1 to 31 characters: lowercase letters, digits and `-`, starting and ending with a letter or digit. It becomes part of the container names and of a DNS alias (see [networking](networking.md)).

Processes are part of the staged config, so adding, changing or removing one is a staged change that ships with the next deploy. Replicas and autoscaling of a process are applied at once, like the scale of a service.

Add, rename or remove processes:

- in the dashboard, under the service's settings, *Processes*;
- in [`nebula.toml`](../reference/nebula-toml.md#processes), as `[services.<slug>.processes.<name>]`;
- with `nebulactrl services processes set <service> <name> --command …`;
- through the API (`config.processes` of the service, `renameProcesses` to rename) and, for agents, through the service tools of MCP.

Processes need a service of kind `app` or `worker` that runs an image or a git build. They do not apply to databases, compose stacks, route services or one-shot services. A service has at most 10 processes, the default one included.

## Ports and domains

A process with a `port` is an HTTP process. Ports are unique across all processes of a service, so a domain's target port names exactly one process.

A domain routes to the **default process** when it has a port, and otherwise to the only process that has a port. If several processes have a port and the domain does not say which, the domain is refused (`process_required`). Name the process on the domain:

- in `nebula.toml`: `process = "<name>"` on the domain;
- in the API and CLI: the `process` field of the domain, `domains add --process`.

A domain keeps its process for its lifetime; to move it, remove it and add it again. A process that a domain still routes to cannot be removed or lose its port, and its name cannot become the default process' name (`process_in_use`). When a process changes to another port, its domains follow, as the default process' domains follow the service's port. A generated hostname for a named process is `<service>-<process>` on the server's base domain.

Renaming a process is a staged change like any other edit of the processes: the running release keeps the old name, with its replica count and its domains, until the next deploy. The new name starts with a copy of the replica settings, and the deploy moves the domains over to it. Discarding the change restores the old name.

## Singleton processes

`singleton = true` makes a process run exactly one instance, and stop the old container **before** the new one starts. Use it for anything that must not run twice: a scheduler, a leader-elected consumer. A singleton cannot be scaled (`replicas_require_stateless`) and has a short gap during each deploy, which is the price of never overlapping.

## Stop grace

`stop_grace_seconds` is how long a container gets to finish after the stop signal before it is killed. A process without the key uses the service's value (30 seconds by default). Give queue workers enough time to finish a job.

## Health, scale and autoscaling

Each process has its own `health_check` with the same keys as the service's. The defaults follow the process: HTTP on its port when it has one, `none` when it has none. A process without a port is healthy when its container stays up for the check period.

Each process scales on its own: `scale.replicas` (1-10) and `scale.autoscaling`, on the service's CPU rules. A process without an HTTP port can be scaled, which is how queue workers scale. A service with volumes or files runs every process as a single instance, and a singleton process always does (`replicas_require_stateless`). See [scaling](scaling.md).

## What a deploy does

One deploy handles every process together:

1. The image is built or pulled **once**.
2. `pre_deploy` runs **once**.
3. Processes with a port start first: new containers start and become healthy, then the proxy switches to them.
4. The remaining processes follow. A singleton stops its old container, then starts the new one.
5. After the last process is healthy, the old containers are retired.
6. `post_deploy` runs **once**.

The deploy is all-or-nothing. If a process fails its health check, the deploy stops, the new containers are removed, the proxy keeps (or goes back to) the previous release and every process of the previous release keeps running. The deployment is `failed`, or `rolled_back` when a process had already switched, and its message names the process that failed. Canceling a deploy does the same.

**Rollback** reverts every process to the target release: the same image, commands, ports and health checks. Replicas are not part of a release, so each process keeps its current replica count; a process the target release does not have is stopped.

If a process disappears from the server (all its containers are gone), the recovery redeploys the current release, all processes together.

See [deployments](deployments.md) for the general flow.

## Logs, metrics and terminal

Logs show every process, each line prefixed `<process> | `, or one process with the process filter (`--process` in the CLI, the process selector in the dashboard, `process=` in the API). Metrics are the service total, or one process with `process=`; samples recorded before the service had processes appear only in the total. The filter follows the running release: until the next deploy, the default process keeps its old name after a staged rename, and a release that runs one process shows the whole service for the default process. The terminal opens a shell in a replica of the chosen process: pick the process, then the replica. See [logs](logs.md) and [terminal](terminal.md).

## Names and variables

Inside the environment network a container is reachable as `<service slug>` (the default process) and `<process>.<service slug>` (a named process). Every container gets `NEBULA_PROCESS` with its process name, so one image can branch on it; see [variables](variables.md).

## Schedulers: a singleton process or a cron task

For "run the scheduler every minute" jobs prefer a [cron task](tasks.md): it needs no always-on container, keeps no state and runs in the service's image with `concurrency = "forbid"`. Choose a singleton process when the scheduler must be resident (`schedule:work`, a clock process, `celery beat`) or needs a resolution finer than a minute.

## Examples

Each example is a complete `nebula.toml`.

### Laravel: web, queue and scheduler

The image serves HTTP; `queue` runs two workers and gets two minutes to finish a job; `scheduler` is a singleton.

<!-- example: processes-laravel -->
```toml
#:schema https://nebula.example.com/schema/nebula.toml.json
schema = 1
server = "fra-1"

[services.shop]
port = 8080                                    # the image's own CMD serves HTTP

[services.shop.git]
url    = "https://github.com/acme/shop.git"
branch = "main"

[services.shop.build]
type = "dockerfile"

[services.shop.pre_deploy]
command = ["php", "artisan", "migrate", "--force"]

[services.shop.domains."shop.acme.com"]

[services.shop.processes.queue]
command            = ["php", "artisan", "queue:work", "--sleep=3", "--tries=3"]
stop_grace_seconds = 120                       # let a running job finish

[services.shop.processes.queue.scale]
replicas = 2

[services.shop.processes.scheduler]
command   = ["php", "artisan", "schedule:work"]
singleton = true                               # two schedulers would run every task twice
```

The resident scheduler can be replaced by a cron task, with no `scheduler` process:

```toml
[services.shop.tasks.schedule]
schedule = "* * * * *"
command  = ["php", "artisan", "schedule:run"]
```

### Rails: puma and sidekiq

<!-- example: processes-rails -->
```toml
#:schema https://nebula.example.com/schema/nebula.toml.json
schema = 1
server = "fra-1"

[services.app]
port    = 3000
command = ["bundle", "exec", "puma", "-C", "config/puma.rb"]

[services.app.git]
url    = "https://github.com/acme/blog.git"
branch = "main"

[services.app.pre_deploy]
command = ["bin/rails", "db:migrate"]

[services.app.domains."blog.acme.com"]

[services.app.processes.sidekiq]
command            = ["bundle", "exec", "sidekiq"]
stop_grace_seconds = 120

[services.app.processes.sidekiq.scale]
replicas = 2
```

### Django: gunicorn, celery worker and celery beat

<!-- example: processes-django -->
```toml
#:schema https://nebula.example.com/schema/nebula.toml.json
schema = 1
server = "fra-1"

[services.portal]
port    = 8000
command = ["gunicorn", "config.wsgi", "--bind", "0.0.0.0:8000"]

[services.portal.git]
url    = "https://github.com/acme/portal.git"
branch = "main"

[services.portal.pre_deploy]
command = ["python", "manage.py", "migrate", "--noinput"]

[services.portal.domains."portal.acme.com"]

[services.portal.processes.worker]
command            = ["celery", "-A", "config", "worker", "--loglevel=info"]
stop_grace_seconds = 60

[services.portal.processes.worker.scale]
replicas = 2

[services.portal.processes.beat]
command   = ["celery", "-A", "config", "beat", "--loglevel=info"]
singleton = true
```

### Node: web and a BullMQ worker

<!-- example: processes-node -->
```toml
#:schema https://nebula.example.com/schema/nebula.toml.json
schema = 1
server = "fra-1"

[services.api]
port    = 3000
command = ["node", "server.js"]

[services.api.git]
url    = "https://github.com/acme/api.git"
branch = "main"

[services.api.domains."api.acme.com"]

[services.api.processes.worker]
command            = ["node", "worker.js"]
stop_grace_seconds = 60

[services.api.processes.worker.scale]
replicas = 3
```

## Updating services to processes: combine

If a project already runs the web server and the worker as separate services built from one repository, **Combine into one service** merges them: select two or more services of the canvas, choose the target and a process name for each of the others. The same flow is in the API (`planCombineServices`, `combineServices`).

The services must be git services of one environment, with the same repository, branch and build settings, on one server with an up-to-date agent. None of them may have processes of its own, the target included, and the other services must have no volumes. The plan lists what changes, which variables conflict (you choose one value per key) and what is ignored in favour of the target's settings. A source that ran without a memory or CPU limit while the target has one is listed too: its process runs with the target's limit.

The combine runs in order, and the other services keep serving until the last step:

1. The target gets the other services as processes and their variables, staged.
2. One deployment of the target builds once and starts every process.
3. When it succeeds, the domains and cron tasks of the other services move to the target in one step and the other services are removed.

If the deployment fails, nothing was deleted and nothing moved: retry or abort. After the cutover there is no abort, and a rollback of the target to a release from before the combine removes the processes without bringing the removed services back.

For a few seconds a worker or scheduler of an old service and the target's process of the same name both run. A scheduler that must not overlap should use *stop the others first*, or the old services should be stopped before.

## Older agents

A server whose agent predates processes refuses a multi-process deploy (`agent_too_old`). Update the agent first; single-process services are not affected.

## Limits

- At most 10 processes per service, the default one included; at most 9 under `processes` in `nebula.toml`.
- Route services, the mesh, a service's own tailnet exposure and mesh links always use the default process and its port.
- Restart, stop and remove act on the whole service, not on one process.
- Volumes, files, Docker options, variables, networks, links, pre- and post-deploy commands and the image retention are shared by every process.
- `clone` and previews copy the processes, but not their replica counts.
- Image-source services cannot be combined.
