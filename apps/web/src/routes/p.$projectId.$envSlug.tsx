import { createFileRoute } from "@tanstack/react-router";
import { useCallback, useEffect } from "react";
import { z } from "zod";
import { CanvasScreen } from "#/features/canvas/canvas-screen";
import {
	type InspectorTab,
	parseTab,
} from "#/features/canvas/inspector/tab-ids";
import { DeploymentDebugHost } from "#/features/deployments/debug/debug-host";
import { logHistorySearch } from "#/features/logs/history/url";
import { processSearch } from "#/features/processes/url";
import { stackSearch } from "#/features/stacks/url";
import { templatesActions } from "#/features/templates/store";
import { vtAnchor } from "#/lib/transitions";

// The inspector's state lives in the address: `?service=<id>&tab=<tab>`, and
// for Settings `&section=<category>`, so a link or a reload lands on the same
// service, tab and category. A malformed value is dropped rather than failing
// the route.
export const Route = createFileRoute("/p/$projectId/$envSlug")({
	validateSearch: z.object({
		service: z.string().optional().catch(undefined),
		tab: z.string().optional().catch(undefined),
		section: z
			.string()
			.regex(/^[a-z][a-z0-9-]*$/)
			.optional()
			.catch(undefined),
		// `?template=<id>` opens the template gallery on that template.
		template: z.string().optional().catch(undefined),
		// `?q=<query>` is the Logs tab's search; see features/logs/search.
		q: z.string().optional().catch(undefined),
		// `?logs=history`, `?range=` and `?from=&to=` are the Logs tab's history.
		...logHistorySearch,
		// `?component=<name>` names a stack's component for the Overview and Logs.
		...stackSearch,
		// `?process=<name>` names a process of a service that runs several, for the Overview, Logs, Metrics and terminal.
		...processSearch,
		// `&debug=<deploymentId>` opens the deployment debug panel over the inspector.
		debug: z.string().optional().catch(undefined),
	}),
	component: CanvasPage,
});

function CanvasPage() {
	const { projectId, envSlug } = Route.useParams();
	const { service, tab, section, template } = Route.useSearch();
	const navigate = Route.useNavigate();

	// A deep link opens the gallery once and then leaves the address clean, so
	// closing the dialog and reloading does not open it again.
	useEffect(() => {
		if (template === undefined) return;
		templatesActions.open(template);
		void navigate({
			search: (previous) => ({ ...previous, template: undefined }),
			replace: true,
		});
	}, [template, navigate]);

	const onInspect = useCallback(
		(
			serviceId: string | undefined,
			nextTab?: InspectorTab,
			nextSection?: string,
		) => {
			void navigate({
				search: (previous) => ({
					...previous,
					service: serviceId,
					// A log search, and its history mode and range, belong to one service.
					...(serviceId === previous.service
						? {}
						: {
								q: undefined,
								logs: undefined,
								range: undefined,
								from: undefined,
								to: undefined,
								component: undefined,
								process: undefined,
								// The debug panel belongs to a deployment of one service.
								debug: undefined,
							}),
					tab: serviceId === undefined ? undefined : (nextTab ?? previous.tab),
					// A category belongs to one service's Settings: it carries over
					// only when the same service stays open on the same tab.
					section:
						serviceId === undefined
							? undefined
							: (nextSection ??
								(serviceId === previous.service && nextTab === undefined
									? previous.section
									: undefined)),
				}),
			});
		},
		[navigate],
	);
	const onTabChange = useCallback(
		(next: InspectorTab) => {
			void navigate({
				search: (previous) => ({
					...previous,
					tab: next,
					section: next === "settings" ? previous.section : undefined,
				}),
				replace: true,
			});
		},
		[navigate],
	);
	const onSectionChange = useCallback(
		(next: string) => {
			void navigate({
				search: (previous) => ({ ...previous, section: next }),
				replace: true,
			});
		},
		[navigate],
	);

	// `data-page` marks the page root; `data-vt-anchor` makes it the shared
	// element a card grows into (and shrinks back to). It stays mounted while
	// the canvas loads.
	return (
		<div
			data-page="canvas"
			{...vtAnchor("project", projectId)}
			className="absolute inset-0"
		>
			<CanvasScreen
				projectId={projectId}
				envSlug={envSlug}
				serviceId={service}
				tab={parseTab(tab)}
				section={section}
				onInspect={onInspect}
				onTabChange={onTabChange}
				onSectionChange={onSectionChange}
			/>
			<DeploymentDebugHost />
		</div>
	);
}
