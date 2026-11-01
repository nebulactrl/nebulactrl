import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
	ActivityIcon,
	AppWindowIcon,
	CopyIcon,
	FolderIcon,
	GitPullRequestIcon,
	LayoutGridIcon,
	LogOutIcon,
	PlusIcon,
	RadioTowerIcon,
	RocketIcon,
	ServerIcon,
} from "lucide-react";
import { useMemo } from "react";
import { projectsQuery, systemQuery } from "#/api/hooks";
import { copyWithToast } from "#/components/common/copy-button";
import { useOpenProject } from "#/components/project/use-open-project";
import { toast } from "#/components/ui/toast";
import { useRegisterCommands } from "./palette-registry";
import { useShellActions } from "./shell-actions";
import { useProjectContext } from "./use-project-context";
import { useSettingsCommands } from "./use-settings-commands";
import { useSignOut } from "./user-menu";

/**
 * The shell's own ⌘K entries: navigation only. Features that act on
 * services (deploy, restart, create) register their own through
 * `useRegisterCommands`.
 */
export function useNavigationCommands() {
	const navigate = useNavigate();
	const { openNewProject } = useShellActions();
	useSettingsCommands();
	const signOut = useSignOut();
	const openProject = useOpenProject();
	const context = useProjectContext();
	const projects = useQuery(projectsQuery());
	const system = useQuery(systemQuery());
	const mcpUrl = system.data?.mcpEnabled ? system.data.mcpUrl : undefined;
	const projectItems = projects.data;
	const projectId = context?.projectId;
	const environments = context?.environments;
	const currentEnvironmentId = context?.environment?.id;
	const envSlug = context?.envSlug;

	const commands = useMemo(() => {
		const entries = [
			{
				id: "go:projects",
				label: "Open projects",
				group: "Go to",
				hint: "all projects",
				icon: FolderIcon,
				keywords: ["home", "dashboard"],
				run: () => void navigate({ to: "/" }),
			},
			{
				id: "go:servers",
				label: "Open servers",
				group: "Go to",
				hint: "hosts running your services",
				icon: ServerIcon,
				keywords: ["hosts", "agents", "machines"],
				run: () => void navigate({ to: "/servers" }),
			},
			{
				id: "action:add-server",
				label: "Add server",
				group: "Action",
				hint: "enroll a Docker host",
				icon: ServerIcon,
				keywords: ["enroll", "agent", "host", "install"],
				run: () => void navigate({ to: "/servers", search: { enroll: true } }),
			},
			{
				id: "go:status-pages",
				label: "Open status pages",
				group: "Go to",
				hint: "public status, incidents, maintenance",
				icon: RadioTowerIcon,
				keywords: ["uptime", "incident", "maintenance", "outage", "public"],
				run: () => void navigate({ to: "/status-pages" }),
			},
			{
				id: "action:new-project",
				label: "New project",
				group: "Action",
				hint: "create",
				icon: PlusIcon,
				run: openNewProject,
			},
			{
				id: "action:copy-mcp",
				label: "Copy MCP endpoint",
				group: "Action",
				hint: mcpUrl ?? "MCP is not enabled",
				icon: CopyIcon,
				keywords: ["model context protocol", "agent", "claude", "url"],
				run: () => {
					if (mcpUrl) void copyWithToast(mcpUrl);
					else {
						toast.add({
							type: "info",
							title: "MCP is not enabled",
							description: "The instance admin turns it on in Admin → MCP.",
						});
					}
				},
			},
			{
				id: "action:sign-out",
				label: "Sign out",
				group: "Action",
				hint: "end this session",
				icon: LogOutIcon,
				run: signOut,
			},
		];
		for (const project of projectItems ?? []) {
			if (project.id === projectId) continue;
			entries.push({
				id: `project:${project.id}`,
				label: project.name,
				group: "Project",
				hint: project.slug,
				icon: LayoutGridIcon,
				run: () => void openProject(project.id),
			});
		}
		if (projectId && envSlug) {
			const params = { projectId, envSlug };
			entries.push(
				{
					id: "go:canvas",
					label: "Open canvas",
					group: "Go to",
					hint: envSlug,
					icon: LayoutGridIcon,
					run: () => void navigate({ to: "/p/$projectId/$envSlug", params }),
				},
				{
					id: "go:deployments",
					label: "Open deployments",
					group: "Go to",
					hint: `every release in ${envSlug}`,
					icon: RocketIcon,
					keywords: ["releases", "history", "rollbacks"],
					run: () =>
						void navigate({
							to: "/p/$projectId/$envSlug/deployments",
							params,
						}),
				},
				{
					id: "go:previews",
					label: "Open previews",
					group: "Go to",
					hint: "pull-request environments",
					icon: GitPullRequestIcon,
					keywords: ["pull request", "pr", "preview", "branch"],
					run: () =>
						void navigate({ to: "/p/$projectId/$envSlug/previews", params }),
				},
				{
					id: "go:observability",
					label: "Open observability",
					group: "Go to",
					hint: "logs and resources",
					icon: ActivityIcon,
					keywords: ["logs", "metrics", "traffic"],
					run: () =>
						void navigate({
							to: "/p/$projectId/$envSlug/observability",
							params,
						}),
				},
			);
		}
		if (projectId) {
			for (const environment of environments ?? []) {
				if (environment.id === currentEnvironmentId) continue;
				entries.push({
					id: `env:${environment.id}`,
					label: `Switch to ${environment.slug}`,
					group: "Go to",
					hint:
						environment.kind === "preview" ? environment.name : "environment",
					icon: AppWindowIcon,
					run: () =>
						void navigate({
							to: "/p/$projectId/$envSlug",
							params: { projectId, envSlug: environment.slug },
						}),
				});
			}
		}
		return entries;
	}, [
		currentEnvironmentId,
		envSlug,
		environments,
		mcpUrl,
		navigate,
		openNewProject,
		openProject,
		projectId,
		projectItems,
		signOut,
	]);

	useRegisterCommands("shell.navigation", commands);
}
