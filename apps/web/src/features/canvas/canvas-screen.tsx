import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { memo, useEffect } from "react";
import { ApiError, errorMessage } from "#/api/client-config";
import { canvasQuery, environmentsQuery } from "#/api/hooks";
import type { Server, Service, ServiceLink } from "#/api/models";
import { ErrorState } from "#/components/common/error-state";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { CreateButton, CreateHost, useCreateService } from "#/features/create";
import { LinkReviewHost } from "#/features/links";
import { MaintenanceHost } from "#/features/maintenance";
import { MeshCanvasProvider } from "#/features/mesh/canvas-mesh";
import { PreviewBanner } from "#/features/previews/preview-banner";
import { EnvironmentConfigBanner } from "#/features/project-config";
import { TasksHost } from "#/features/tasks";
import { TemplatesHost } from "#/features/templates/templates-host";
import { TerminalHost } from "#/features/terminal";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { whenIdle } from "#/lib/when-idle";
import { CanvasFloor } from "./canvas-floor";
import { useLinks, useServers, useServices } from "./data";
import { CanvasEmptyState } from "./empty-state";
import { preloadOverview } from "./inspector/tab-bodies";
import type { InspectorTab } from "./inspector/tab-ids";
import { emptyLayout } from "./lib/layout";
import { ProjectCanvas } from "./project-canvas";
import { useCanvasCommands } from "./use-canvas-commands";

function GoToProjects() {
	return (
		<Button variant="secondary" render={<Link to="/" />}>
			Go to projects
		</Button>
	);
}

const NO_LINKS: ServiceLink[] = [];
const NO_SERVERS: Server[] = [];
const NO_SERVICES: Service[] = [];

/** Built once: a fresh element per render would defeat the canvas's props check. */
const TOOLBAR_END = <CreateButton />;

/**
 * The dialogs and sheets the canvas can open. They take no props, so the
 * screen re-rendering on every `?service=` change skips all of them.
 */
const CanvasHosts = memo(function CanvasHosts() {
	return (
		<>
			<CreateHost />
			<TemplatesHost />
			<LinkReviewHost />
			<MaintenanceHost />
			<TasksHost />
			<TerminalHost />
		</>
	);
});

export interface CanvasScreenProps {
	projectId: string;
	envSlug: string;
	/** `?service=`: the inspected service. */
	serviceId: string | undefined;
	tab: InspectorTab;
	/** `?section=`: the Settings category the inspector shows. */
	section: string | undefined;
	/**
	 * Opens a service's inspector (on `tab`, and in Settings on `section`), or
	 * with undefined closes it.
	 */
	onInspect(
		serviceId: string | undefined,
		tab?: InspectorTab,
		section?: string,
	): void;
	onTabChange(tab: InspectorTab): void;
	onSectionChange(section: string): void;
}

/** The floor a canvas that has not (or cannot) load shows behind its message. */
function Backdrop({ production = false }: { production?: boolean }) {
	return <CanvasFloor production={production} />;
}

/**
 * Everything the canvas route does besides drawing: resolve the environment
 * from its slug, load its services, links, servers and saved layout, and
 * stand in for the canvas while any of that is loading or has failed. The
 * canvas only mounts with real data, so a failed load can never be mistaken
 * for "every service was deleted" and prune a saved layout.
 */
export function CanvasScreen({
	projectId,
	envSlug,
	serviceId,
	tab,
	section,
	onInspect,
	onTabChange,
	onSectionChange,
}: CanvasScreenProps) {
	const environments = useQuery(environmentsQuery(projectId));
	const environment = environments.data?.find(
		(candidate) => candidate.slug === envSlug,
	);
	const environmentId = environment?.id;
	useDocumentTitle([environment?.name, "Architecture"]);

	const services = useServices(environmentId);
	const links = useLinks(environmentId);
	const servers = useServers();
	const layout = useQuery({
		...canvasQuery(projectId, environmentId ?? ""),
		enabled: environmentId !== undefined,
	});
	// The create flow, the link review and the ⌘K entries all act on this
	// environment's services.
	const create = useCreateService();
	const serviceList = services.data ?? NO_SERVICES;

	useCanvasCommands({
		services: serviceList,
		isProduction: environment?.isProduction ?? false,
		onInspect,
		onCreate: () => create.open(),
	});

	// Fetch the Overview chunk(s) once the canvas has painted and the browser is
	// idle, so the first click on a card mounts a body that is already there.
	const hasServices = serviceList.length > 0;
	const hasRoute = serviceList.some((service) => service.kind === "route");
	useEffect(() => {
		if (!hasServices) return;
		return whenIdle(() => {
			preloadOverview({ kind: "app" });
			if (hasRoute) preloadOverview({ kind: "route" });
		});
	}, [hasServices, hasRoute]);

	if (environments.isError) {
		// A missing (or inaccessible) project cannot be fixed by retrying.
		const missing =
			environments.error instanceof ApiError &&
			environments.error.status === 404;
		return (
			<>
				<Backdrop />
				{missing ? (
					<ErrorState
						neutral
						className="min-h-full"
						title="Project not found"
						body="It may have been deleted, or you do not have access."
						action={<GoToProjects />}
					/>
				) : (
					<ErrorState
						neutral
						className="min-h-full"
						title="Couldn't load this project"
						body={errorMessage(environments.error)}
						retry={() => void environments.refetch()}
					/>
				)}
			</>
		);
	}
	if (environments.isSuccess && environment === undefined) {
		return (
			<>
				<Backdrop />
				<ErrorState
					neutral
					className="min-h-full"
					title="Environment not found"
					body={`This project has no environment called “${envSlug}”.`}
					action={<GoToProjects />}
				/>
			</>
		);
	}
	if (environment === undefined) {
		return (
			<>
				<Backdrop />
				<Skeleton className="absolute top-3 left-3 h-9 w-64 rounded-[10px]" />
			</>
		);
	}

	// Services that cannot be listed: an empty canvas with the reason beside
	// its invitation. A 404 means the control plane has no such list yet.
	if (services.data === undefined) {
		if (services.isError) {
			const notFound =
				services.error instanceof ApiError && services.error.status === 404;
			return (
				<>
					<Backdrop production={environment.isProduction} />
					<CanvasEmptyState
						error={{
							message: notFound
								? "the control plane has no services endpoint for this environment (404)."
								: errorMessage(services.error),
							onRetry: () => void services.refetch(),
						}}
					/>
					<CreateHost />
					<TemplatesHost />
				</>
			);
		}
		return <Backdrop production={environment.isProduction} />;
	}
	// The layout is written back as the reader edits it, so wait for what
	// was saved before drawing; a layout that cannot be read starts blank.
	if (layout.isPending) {
		return <Backdrop production={environment.isProduction} />;
	}

	return (
		<MeshCanvasProvider
			environmentId={environment.id}
			services={services.data}
			links={links.data ?? NO_LINKS}
		>
			<ProjectCanvas
				key={environment.id}
				projectId={projectId}
				environmentId={environment.id}
				isProduction={environment.isProduction}
				environmentName={environment.name}
				services={services.data}
				links={links.data ?? NO_LINKS}
				servers={servers.data ?? NO_SERVERS}
				document={layout.data ?? { layout: emptyLayout(), version: 0 }}
				inspectedServiceId={serviceId}
				tab={tab}
				section={section}
				onInspect={onInspect}
				onTabChange={onTabChange}
				onSectionChange={onSectionChange}
				toolbarEnd={TOOLBAR_END}
			/>
			<PreviewBanner environment={environment} />
			<EnvironmentConfigBanner environment={environment} />
			<CanvasHosts />
		</MeshCanvasProvider>
	);
}
