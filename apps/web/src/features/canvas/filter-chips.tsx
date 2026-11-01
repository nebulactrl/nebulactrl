import { cn } from "cn";
import { memo, type ReactNode, useMemo } from "react";
import type { Server, Service, ServiceKind } from "#/api/models";
import { StatusIcon } from "#/components/ui/status-icon";
import {
	type CanvasFilter,
	isFilterEmpty,
	matchesFilter,
	matchSummary,
	NO_SERVER,
} from "./lib/filter";
import { nodeStatus, STATUS_GROUP_LABEL, type StatusGroup } from "./lib/status";

const KIND_LABEL: Record<ServiceKind, string> = {
	app: "App",
	worker: "Worker",
	database: "Database",
	route: "Route",
	stack: "Compose stack",
};

const STATUS_ORDER: StatusGroup[] = [
	"deployed",
	"in_flight",
	"waiting",
	"failed",
	"rolled_back",
	"not_deployed",
];

function toggled<T>(set: ReadonlySet<T>, value: T): Set<T> {
	const next = new Set(set);
	if (next.has(value)) next.delete(value);
	else next.add(value);
	return next;
}

function Chip({
	pressed,
	onClick,
	children,
}: {
	pressed: boolean;
	onClick(): void;
	children: ReactNode;
}) {
	return (
		<button
			type="button"
			aria-pressed={pressed}
			onClick={onClick}
			className={cn(
				"flex h-6 shrink-0 cursor-pointer items-center gap-1.5 rounded-full border px-2.5 font-medium text-[12px] leading-none transition-[color,background-color,border-color,scale] duration-(--press-duration) ease-out focus-visible:focus-ring not-disabled:active:scale-(--press-scale)",
				pressed
					? "border-primary-hairline bg-[color-mix(in_oklab,var(--color-surface-float),var(--color-accent-ink)_16%)] text-foreground"
					: "border-line bg-surface-float text-muted-foreground hover:border-line-strong hover:text-foreground",
			)}
		>
			{children}
		</button>
	);
}

function Divider() {
	return <span aria-hidden className="h-4 w-px shrink-0 bg-border" />;
}

/**
 * Filter chips for the cards on the canvas: status, server and kind. Chips
 * within a row are alternatives; rows combine. Cards that do not match dim
 * rather than disappear, so the shape of the environment stays readable.
 */
export const FilterChips = memo(function FilterChips({
	services,
	servers,
	filter,
	onChange,
}: {
	services: readonly Service[];
	servers: readonly Server[];
	filter: CanvasFilter;
	onChange(filter: CanvasFilter): void;
}) {
	const { statuses, hosts, kinds } = useMemo(() => {
		const statusSet = new Set<StatusGroup>();
		const hostMap = new Map<string, { name: string; status: string }>();
		const kindSet = new Set<ServiceKind>();
		const live = new Map(servers.map((server) => [server.id, server]));
		for (const service of services) {
			statusSet.add(nodeStatus(service).group);
			kindSet.add(service.kind);
			const id = service.serverId ?? NO_SERVER;
			if (!hostMap.has(id)) {
				hostMap.set(id, {
					name: service.server?.name ?? "No server",
					status: live.get(id)?.status ?? service.server?.status ?? "pending",
				});
			}
		}
		return {
			statuses: STATUS_ORDER.filter((group) => statusSet.has(group)),
			hosts: [...hostMap.entries()],
			kinds: (Object.keys(KIND_LABEL) as ServiceKind[]).filter((kind) =>
				kindSet.has(kind),
			),
		};
	}, [services, servers]);

	if (services.length === 0) return null;
	// One of everything: nothing to filter by, so no empty bar.
	if (
		statuses.length < 2 &&
		hosts.length < 2 &&
		kinds.length < 2 &&
		isFilterEmpty({ ...filter, query: "" })
	) {
		return null;
	}

	const matching = services.filter((service) =>
		matchesFilter(service, filter),
	).length;

	return (
		<fieldset
			aria-label="Filter services"
			className="surface-float m-0 min-w-0 border pointer-events-auto flex max-w-full items-center gap-1.5 overflow-x-auto rounded-full px-2 py-1"
		>
			<span className="shrink-0 pl-1 font-medium text-[12px] text-ink-3">
				Show
			</span>
			{statuses.length > 1
				? statuses.map((group) => (
						<Chip
							key={group}
							pressed={filter.statuses.has(group)}
							onClick={() =>
								onChange({
									...filter,
									statuses: toggled(filter.statuses, group),
								})
							}
						>
							{STATUS_GROUP_LABEL[group]}
						</Chip>
					))
				: null}
			{statuses.length > 1 && (hosts.length > 1 || kinds.length > 1) ? (
				<Divider />
			) : null}
			{hosts.length > 1
				? hosts.map(([id, host]) => (
						<Chip
							key={id}
							pressed={filter.serverIds.has(id)}
							onClick={() =>
								onChange({
									...filter,
									serverIds: toggled(filter.serverIds, id),
								})
							}
						>
							{host.name}
							{host.status === "online" ? null : (
								<StatusIcon
									kind={host.status === "pending" ? "pending" : "offline"}
								/>
							)}
						</Chip>
					))
				: null}
			{hosts.length > 1 && kinds.length > 1 ? <Divider /> : null}
			{kinds.length > 1
				? kinds.map((kind) => (
						<Chip
							key={kind}
							pressed={filter.kinds.has(kind)}
							onClick={() =>
								onChange({ ...filter, kinds: toggled(filter.kinds, kind) })
							}
						>
							{KIND_LABEL[kind]}
						</Chip>
					))
				: null}
			{!isFilterEmpty({ ...filter, query: "" }) ? (
				<button
					type="button"
					onClick={() =>
						onChange({
							...filter,
							statuses: new Set(),
							serverIds: new Set(),
							kinds: new Set(),
						})
					}
					className="shrink-0 cursor-pointer px-1.5 text-[12px] text-primary-text hover:text-inverse-hover focus-visible:focus-ring"
				>
					Clear
				</button>
			) : null}
			{/* While a search is typed its field prints the count; two would repeat it. */}
			{filter.query.trim() === "" && !isFilterEmpty(filter) ? (
				<span
					role="status"
					className="shrink-0 pr-1 text-[12px] text-ink-3 tabular-nums"
				>
					{matchSummary(matching, services.length)}
				</span>
			) : null}
		</fieldset>
	);
});
