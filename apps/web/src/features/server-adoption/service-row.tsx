import { cn } from "cn";
import { DatabaseIcon, HardDriveIcon } from "lucide-react";
import { useId, useState } from "react";
import type { AdoptionChoices, AdoptionService } from "#/api/gen/types.gen";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { Switch } from "#/components/ui/switch";
import { ServiceIcon } from "#/features/canvas/service-icon";
import {
	ACTION_LABEL,
	actionOf,
	KIND_LABEL,
	patchService,
	type ServiceAction,
	setHostSkipped,
	setSecret,
	sourceLine,
} from "./lib";

const ACTIONS: readonly ServiceAction[] = ["adopt", "leave", "remove"];

const SECRET_REASON: Record<string, string> = {
	name: "by its name",
	"url credentials": "its value carries credentials",
	"database password": "database password",
	choice: "your choice",
};

function iconSource(service: AdoptionService) {
	if (service.database) {
		return {
			kind: "database",
			config: { database: { engine: service.database.engine } },
		};
	}
	return {
		kind: service.kind,
		sourceType: service.sourceType,
		config: { image: service.image },
	};
}

function Group({
	title,
	children,
}: {
	title: string;
	children: React.ReactNode;
}) {
	return (
		<section aria-label={title} className="flex flex-col gap-1.5">
			<h4 className="type-label m-0">{title}</h4>
			{children}
		</section>
	);
}

/** A mono line of text per item, in a quiet list. */
function MonoList({ items }: { items: readonly React.ReactNode[] }) {
	return (
		<ul className="m-0 flex list-none flex-col gap-0.5 p-0 font-mono text-[12px] text-ink-2">
			{items.map((item, index) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: a static list of lines, never reordered.
				<li key={index}>{item}</li>
			))}
		</ul>
	);
}

/**
 * One service found on the host: its name and what it runs, the choice for
 * it (adopt, leave running, remove) and, opened, everything adoption would
 * carry over: containers, volumes, domains, variable names and notes.
 */
export function ServiceRow({
	service,
	choices,
	onChoices,
	locked,
}: {
	service: AdoptionService;
	choices: AdoptionChoices;
	onChoices(update: (current: AdoptionChoices) => AdoptionChoices): void;
	/** An apply is in flight: every control is off. */
	locked: boolean;
}) {
	const id = useId();
	const [open, setOpen] = useState(false);
	const choice = choices.services?.[service.id];
	const action = actionOf(service, choice);
	const reattach = action === "reattach";
	const containers = service.containers ?? [];
	const volumes = service.volumes ?? [];
	const domains = service.domains ?? [];
	const env = service.env ?? [];
	const notes = service.notes ?? [];
	const skipped = new Set(choice?.skipHosts ?? []);
	const included = domains.filter((d) => d.include && !skipped.has(d.host));
	const dimmed = action === "leave" || action === "remove";

	return (
		<li
			data-slot="adoption-service-row"
			data-action={action}
			className="surface-well flex flex-col gap-2.5 rounded-ctl border border-line px-3 py-2.5"
		>
			<div className="flex flex-wrap items-start gap-x-3 gap-y-2">
				<ServiceIcon service={iconSource(service)} size={30} />
				<div className="flex min-w-48 flex-1 flex-col gap-1">
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
						<Input
							aria-label={`Name of ${service.slug}`}
							autoComplete="off"
							spellCheck={false}
							value={choice?.name ?? service.name}
							disabled={locked || action === "leave" || action === "remove"}
							onChange={(event) =>
								onChoices((current) =>
									patchService(current, service.id, {
										name: event.target.value,
									}),
								)
							}
							className="h-7 max-w-64"
						/>
						<span className="font-mono text-[12px] text-ink-3">
							{service.slug}
						</span>
						<Badge>{KIND_LABEL[service.kind]}</Badge>
						{reattach ? <Badge variant="accent">already here</Badge> : null}
						{!service.adoptable && !reattach ? (
							<Badge variant="warning">not adoptable</Badge>
						) : null}
					</div>
					<div
						className={cn(
							"flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-2xs text-ink-3",
							dimmed && "opacity-70",
						)}
					>
						<span className="flex min-w-0 items-center gap-1 break-all">
							{service.database ? (
								<DatabaseIcon aria-hidden className="size-3 shrink-0" />
							) : null}
							{sourceLine(service)}
						</span>
						<span className="tabular-nums">
							{service.replicas}{" "}
							{service.replicas === 1 ? "replica" : "replicas"}
						</span>
						{included.length > 0 ? (
							<span className="break-all">
								{included.map((d) => d.host).join(", ")}
							</span>
						) : null}
						{volumes.length > 0 ? (
							<span className="flex items-center gap-1">
								<HardDriveIcon aria-hidden className="size-3" />
								{volumes.length} {volumes.length === 1 ? "volume" : "volumes"}
							</span>
						) : null}
					</div>
				</div>
				{reattach ? null : (
					<Segmented
						aria-label={`What to do with ${service.slug}`}
						font="sans"
						value={action as ServiceAction}
						onValueChange={(next) =>
							onChoices((current) =>
								patchService(current, service.id, { action: next }),
							)
						}
						options={ACTIONS.map((value) => ({
							value,
							label: ACTION_LABEL[value],
							disabled: locked || (value === "adopt" && !service.adoptable),
						}))}
					/>
				)}
				<button
					type="button"
					aria-expanded={open}
					aria-controls={`${id}-body`}
					onClick={() => setOpen(!open)}
					className="type-label flex shrink-0 cursor-pointer items-center gap-1 rounded-sm px-1 py-1 outline-none hover:text-ink-1 focus-visible:focus-ring"
				>
					Details
					<DisclosureChevron open={open} className="size-3.5" />
				</button>
			</div>

			{!service.adoptable && !reattach && service.reason ? (
				<p role="note" className="m-0 text-[12px] text-ink-2 leading-[1.5]">
					{service.reason}
				</p>
			) : null}
			{reattach ? (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					This instance already has this service. It is pointed at this server;
					nothing is recreated.
				</p>
			) : null}
			{action === "leave" ? (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					The containers keep running, unmanaged. NebulaCtrl never removes them.
				</p>
			) : null}
			{action === "remove" ? (
				<p className="m-0 text-[12px] text-ink-2 leading-[1.5]">
					The containers are removed after adoption; volumes stay.
				</p>
			) : null}

			<DisclosurePanel open={open} id={`${id}-body`}>
				<div className="flex flex-col gap-4 pt-1">
					{containers.length > 0 ? (
						<Group title="Containers">
							<ul className="m-0 flex list-none flex-col gap-0.5 p-0 font-mono text-[12px] text-ink-2">
								{containers.map((container) => (
									<li
										key={container.name}
										className="flex flex-wrap items-center gap-x-2"
									>
										<span className="break-all">{container.name}</span>
										<span className="text-ink-3">{container.state}</span>
										{container.leftover ? (
											<Badge variant="outline">older, removed after</Badge>
										) : null}
									</li>
								))}
							</ul>
						</Group>
					) : null}

					{volumes.length > 0 ? (
						<Group title="Volumes">
							<MonoList
								items={volumes.map(
									(volume) => `${volume.name} → ${volume.mountPath}`,
								)}
							/>
						</Group>
					) : null}

					{domains.length > 0 ? (
						<Group title="Domains">
							<ul className="m-0 flex list-none flex-col gap-2 p-0">
								{domains.map((domain) => {
									const skip = skipped.has(domain.host);
									const checked = domain.include && !skip;
									return (
										<li
											key={`${domain.host}${domain.pathPrefix ?? ""}`}
											className="flex flex-col gap-1 border-line-faint border-t pt-2 first:border-t-0 first:pt-0"
										>
											<div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
												<Switch
													size="sm"
													aria-label={`Adopt ${domain.host}${domain.pathPrefix ?? ""}`}
													checked={checked}
													disabled={locked || (!domain.include && !skip)}
													onCheckedChange={(next) =>
														onChoices((current) =>
															setHostSkipped(
																current,
																service.id,
																domain.host,
																!next,
															),
														)
													}
												/>
												<span className="break-all font-mono text-[12px] text-ink-1">
													{domain.host}
													{domain.pathPrefix ?? ""}
												</span>
												{domain.targetPort ? (
													<span className="font-mono text-2xs text-ink-3">
														port {domain.targetPort}
													</span>
												) : null}
												<span className="font-mono text-2xs text-ink-3">
													{checked ? "adopted" : "left out"}
												</span>
											</div>
											{domain.note ? (
												<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
													{domain.note}
												</p>
											) : null}
										</li>
									);
								})}
							</ul>
						</Group>
					) : null}

					{env.length > 0 ? (
						<Group title="Variables">
							<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
								Values are read from the running containers and never shown
								here. A secret is stored encrypted and hidden afterwards.
							</p>
							<ul className="m-0 flex list-none flex-col gap-1 p-0">
								{env.map((variable) => {
									const secret =
										choice?.secret?.[variable.key] ?? variable.secret;
									return (
										<li
											key={variable.key}
											className="flex flex-wrap items-center gap-x-3 gap-y-1"
										>
											<Switch
												size="sm"
												aria-label={`${variable.key} is a secret`}
												checked={secret}
												disabled={locked}
												onCheckedChange={(next) =>
													onChoices((current) =>
														setSecret(current, service.id, variable.key, next),
													)
												}
											/>
											<span className="break-all font-mono text-[12px] text-ink-1">
												{variable.key}
											</span>
											<span className="text-[12px] text-ink-3">
												{secret
													? variable.secret && variable.secretReason
														? `secret: ${SECRET_REASON[variable.secretReason] ?? variable.secretReason}`
														: "secret"
													: "plain"}
											</span>
										</li>
									);
								})}
							</ul>
						</Group>
					) : null}

					{notes.length > 0 ? (
						<Alert variant="info" role="note">
							<AlertDescription>
								<ul className="m-0 flex list-disc flex-col gap-1 pl-4">
									{notes.map((note) => (
										<li key={note}>{note}</li>
									))}
								</ul>
							</AlertDescription>
						</Alert>
					) : null}
				</div>
			</DisclosurePanel>
		</li>
	);
}
