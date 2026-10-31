import type { Server } from "#/api/models";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { StatusIcon } from "#/components/ui/status-icon";

/** Servers a route can be published on: enrolled, not revoked. */
export function routeServers(servers: readonly Server[]): Server[] {
	return servers.filter(
		(server) => server.status !== "revoked" && server.status !== "pending",
	);
}

/**
 * A server menu for route dialogs. A server whose agent predates proxy v2
 * stays listed but cannot be chosen, and says why.
 */
export function ServerSelect({
	id,
	servers,
	value,
	onChange,
	invalid,
}: {
	id: string;
	servers: readonly Server[];
	value: string;
	onChange(serverId: string): void;
	invalid?: boolean;
}) {
	const items = servers.map((server) => ({
		value: server.id,
		label: server.name,
	}));
	return (
		<Select
			value={value === "" ? null : value}
			items={items}
			onValueChange={(next) => onChange(String(next ?? ""))}
		>
			<SelectTrigger id={id} className="w-full" aria-invalid={invalid === true}>
				<SelectValue placeholder="Pick a server" />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false}>
				{servers.map((server) => {
					const old = server.status === "online" && !server.proxy.v2;
					return (
						<SelectItem key={server.id} value={server.id} disabled={old}>
							<span className="flex items-center gap-2">
								<StatusIcon
									kind={server.status === "online" ? "live" : "offline"}
									label=""
								/>
								<span>{server.name}</span>
								{old ? (
									<span className="text-2xs text-warning-text">
										agent too old: upgrade it first
									</span>
								) : server.status !== "online" ? (
									<span className="text-2xs text-ink-3">{server.status}</span>
								) : null}
							</span>
						</SelectItem>
					);
				})}
			</SelectContent>
		</Select>
	);
}
