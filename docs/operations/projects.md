---
title: Projects
description: "Deleting a project, either empty or with everything in it."
section: Applications
order: 5
---

# Projects

A project holds environments, and the environments hold services, databases, domains and variables. This page covers deleting one.

## Delete from the dashboard

Open the project's **Settings** (in the sidebar's Project group) and choose **Delete project** under **Danger zone**. The same action is in the `⋯` menu on each card of the projects page (also on right-click, or the context-menu key or Shift+F10 on a focused card), and in the `⋯` menu in the top bar of a project.

The dialog lists what goes: the environments by name, the services and databases, the volumes and their data, the domains and their DNS records, the previews and the status page components. It says that backups already written to your destinations are kept. **Delete project** stays disabled until you type the project's name. An error from the server, such as a deployment still running, appears in the dialog and nothing is deleted.

## Delete an empty project

`DELETE /projects/{projectId}` removes a project that has no services. It fails with `409 project_not_empty` while any environment still has one. The project's environments and its shared variables go with it.

## Delete a project with everything in it

`DELETE /projects/{projectId}?cascade=true&confirm=<project name>` removes the project and everything in it. It cannot be undone. It returns `204`.

`confirm` must equal the project's **name** exactly, including case. Anything else is `422 confirm_mismatch` and nothing is deleted. Without `cascade`, `confirm` is ignored.

What it removes, in this order:

1. **Previews.** Every pull-request preview, with its services, volumes, domains and environment, the same way [deleting a preview](previews.md) does.
2. **Services.** Every service in every environment, databases included, the way deleting a service with **Delete volumes** ticked does. Each service is removed from the database and its server is told to remove the container and its data volumes. Domains go with the service.
3. **DNS records.** The records NebulaCtrl manages in a connected Cloudflare zone for those domains are removed through the same job that follows deleting a domain. Records it does not manage are not touched. See [Cloudflare](cloudflare.md).
4. **Status page components.** The components that show the project's services are removed from their status pages. The pages themselves, their other components, incidents and settings stay as they are.
5. **Shared variables** of the project and of each of its environments.
6. **Environments**, then **the project**.

What stays:

- **Backups already written to a backup destination** (S3 storage or a server directory). Only the project's backup history in NebulaCtrl goes. Delete the stored objects from the destination if you do not need them. See [database backups](database-backups.md).
- Anything on a server that the agent could not remove, for example while the server is offline: the removal waits in the server's command queue and runs when the agent is back.
- Audit entries.

Each service is deleted in its own transaction. The project, its environments, its shared variables and the status page components are removed in one more. If the call fails halfway (a server error, say), the services already deleted stay deleted, and the project can be deleted again.

### Refused before anything is deleted

| Code | Why |
|---|---|
| `409 deployment_in_progress` | A service of the project, or of one of its previews, is being deployed. Wait for it or cancel it. |
| `409 project_has_replicas` | A database of the project has read replicas. Remove them first with **Remove replica**, so their replication slots are dropped. See [database replicas](database-replicas.md). |
| `409 system_project` | The platform project, which holds the team's Headscale service if it set one up, cannot be deleted. See [Private network (Headscale)](platform-services.md#private-network-headscale). |
| `422 confirm_mismatch` | `confirm` is not the project's name. |

### See what it removes first

`GET /projects/{projectId}/deletion-plan` (needs `projects:read`) counts what a cascade delete removes and deletes nothing:

```json
{
  "project": { "id": "…", "name": "Shop", "slug": "shop" },
  "environments": [{ "id": "…", "name": "production", "production": true }],
  "services": 4, "databases": 1, "volumes": 3, "domains": 2, "previews": 1,
  "statusPageComponents": 2, "managedDnsRecords": 2,
  "backupDestinationsKept": true
}
```

`services` does not include databases. The counts cover the whole project, previews included; `environments` lists the project's own environments, not its previews. `backupDestinationsKept` is always `true`: backups already written to a destination stay.

The dashboard shows these counts in the delete dialog of the project's settings page and in the context menu on the projects page, and asks you to type the project's name.

## Production approval

A project with a production environment is guarded for MCP principals, like deleting a service or an environment there. The first call returns `409 approval_required` with a plan that lists what goes (services, databases, previews, volumes, domains, DNS records); a person approves it in the web app, and the retry then deletes everything. The approval covers the services and previews, so nobody is asked once per service. People signed in to the dashboard are not asked. See [production approval](../reference/mcp.md#production-approval).

The MCP tool is `project_delete`, with `projectId`, `cascade` (default `false`) and `confirm`. `getProjectDeletionPlan` is available through `call_operation`.

## Audit

A cascade delete writes one `project.deleted` entry with the counts (`environments`, `services`, `databases`, `volumes`, `domains`, `previews`, `statusPageComponents`, `managedDnsRecords`), next to the `service.delete` and `preview.delete` entries of what it removed. Deleting an empty project writes `project.delete`.

## Environment names

An environment's address is made from its name, and the project's settings page lives at `/p/{project}/settings`. Creating or cloning an environment whose address would be `settings` (a name like "Settings") is refused with `422 slug_reserved`. The address of an environment never changes, so renaming one cannot take it.
