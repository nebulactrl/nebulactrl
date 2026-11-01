import type { Environment, Server } from "#/api/models";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { isSchedulable } from "#/features/fleet/model";

/** Regular environments of a project; a pull-request preview is not a target. */
export function targetEnvironments(
	environments: readonly Environment[],
): Environment[] {
	return environments.filter((environment) => environment.kind !== "preview");
}

export function EnvironmentSelect({
	id,
	environments,
	value,
	onChange,
	exclude,
}: {
	id: string;
	environments: readonly Environment[];
	value: string;
	onChange(environmentId: string): void;
	/** An environment that cannot be picked (a move's own environment). */
	exclude?: string;
}) {
	const choices = targetEnvironments(environments).filter(
		(environment) => environment.id !== exclude,
	);
	const items = choices.map((environment) => ({
		value: environment.id,
		label: environment.name,
	}));
	return (
		<Select
			value={value === "" ? null : value}
			items={items}
			onValueChange={(next) => onChange(String(next ?? ""))}
		>
			<SelectTrigger id={id} className="w-full">
				<SelectValue placeholder="Pick an environment" />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false}>
				{choices.map((environment) => (
					<SelectItem key={environment.id} value={environment.id}>
						<span className="flex items-center gap-2">
							<span>{environment.name}</span>
							{environment.isProduction ? (
								<span className="text-2xs text-ink-3">production</span>
							) : null}
						</span>
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}

/** The value of "keep each service on the server it has". */
export const SAME_SERVER = "same";

export function ServerChoice({
	id,
	servers,
	value,
	onChange,
	sameLabel,
}: {
	id: string;
	servers: readonly Server[];
	value: string;
	onChange(serverId: string): void;
	sameLabel: string;
}) {
	const choices = servers.filter((server) => isSchedulable(server));
	const items = [
		{ value: SAME_SERVER, label: sameLabel },
		...choices.map((server) => ({ value: server.id, label: server.name })),
	];
	return (
		<Select
			value={value}
			items={items}
			onValueChange={(next) => onChange(String(next ?? SAME_SERVER))}
		>
			<SelectTrigger id={id} className="w-full">
				<SelectValue />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false}>
				<SelectItem value={SAME_SERVER}>{sameLabel}</SelectItem>
				{choices.map((server) => (
					<SelectItem key={server.id} value={server.id}>
						<span className="flex items-center gap-2">
							<span>{server.name}</span>
							{server.status !== "online" ? (
								<span className="text-2xs text-ink-3">{server.status}</span>
							) : null}
						</span>
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}
