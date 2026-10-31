import { cn } from "cn";
import {
	DatabaseIcon,
	GitBranchIcon,
	HardDriveIcon,
	NetworkIcon,
	TimerIcon,
} from "lucide-react";
import { useId } from "react";
import type { ComposeChoices, ComposePlanService } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { Checkbox } from "#/components/ui/checkbox";
import { Input } from "#/components/ui/input";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { EnvTable } from "./env-table";
import {
	domainEnabled,
	isIncluded,
	publishedKeys,
	setInclude,
	setServiceName,
	sourceChip,
} from "./lib";
import { NoteAlerts } from "./parts";
import {
	NetworkSection,
	RuntimeSection,
	SourceSection,
	StorageSection,
} from "./service-sections";

interface ServiceCardProps {
	service: ComposePlanService;
	choices: ComposeChoices;
	onChoices(update: (current: ComposeChoices) => ComposeChoices): void;
}

/**
 * One Compose service as it would be imported: include switch, name, what it
 * runs, then its source, network, storage, runtime, variables and the
 * settings that could not be carried over. An excluded service folds down to
 * its header.
 */
export function ServiceCard({ service, choices, onChoices }: ServiceCardProps) {
	const nameId = useId();
	const included = isIncluded(service, choices);
	const choice = choices.services?.[service.key];
	const chip = sourceChip(service, choices);
	const published = publishedKeys(service, choices);
	const image =
		chip.kind === "image" ? (choice?.image ?? service.source.image ?? "") : "";
	const facts = [
		service.portHttp && service.port > 0 ? `http :${service.port}` : null,
		domainEnabled(service, choices) ? "domain" : null,
		published.length > 0 ? `publishes ${published.join(", ")}` : null,
		(service.volumes ?? []).length > 0
			? `${(service.volumes ?? []).length} ${(service.volumes ?? []).length === 1 ? "volume" : "volumes"}`
			: null,
	].filter((fact): fact is string => fact !== null);

	return (
		<li
			data-slot="compose-service-card"
			data-included={included}
			className="surface-raised flex flex-col gap-3 rounded-card border border-line px-4 py-3.5"
		>
			<div className="flex items-start gap-3">
				<Checkbox
					aria-label={`Include ${service.name}`}
					checked={included}
					onCheckedChange={(next) =>
						onChoices((current) =>
							setInclude(current, service.key, next === true),
						)
					}
					className="mt-2"
				/>
				<ServiceIcon
					service={{
						kind: service.kind,
						...(image ? { config: { image } } : {}),
					}}
					size={32}
				/>
				<div className="flex min-w-0 flex-1 flex-col gap-1.5">
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
						<label htmlFor={nameId} className="sr-only">
							Name of {service.key}
						</label>
						<Input
							id={nameId}
							autoComplete="off"
							spellCheck={false}
							disabled={!included}
							placeholder={service.key}
							value={choice?.name ?? service.name}
							onChange={(event) =>
								onChoices((current) =>
									setServiceName(current, service.key, event.target.value),
								)
							}
							className="h-7 w-48 max-w-full font-medium text-ink-1"
						/>
						<Badge variant={chip.kind === "git" ? "accent" : "default"}>
							{chip.kind === "managed" ? (
								<DatabaseIcon aria-hidden />
							) : chip.kind === "git" ? (
								<GitBranchIcon aria-hidden />
							) : null}
							{chip.text}
						</Badge>
						{service.oneShot ? (
							<Badge variant="accent">
								<TimerIcon aria-hidden />
								One-shot job
							</Badge>
						) : service.kind === "worker" ? (
							<Badge>Worker</Badge>
						) : null}
						{service.key !== service.name &&
						service.key !== (choice?.name ?? service.name) ? (
							<span className="font-mono text-2xs text-ink-4">
								compose: {service.key}
							</span>
						) : null}
					</div>
					<div
						className={cn(
							"flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-2xs text-ink-3 tabular-nums",
							!included && "opacity-70",
						)}
					>
						<span>slug {service.slug}</span>
						{facts.map((fact) => (
							<span key={fact} className="flex items-center gap-1">
								{fact.startsWith("publishes") ? (
									<NetworkIcon aria-hidden className="size-3 text-accent-ink" />
								) : fact.endsWith("volume") || fact.endsWith("volumes") ? (
									<HardDriveIcon aria-hidden className="size-3" />
								) : null}
								{fact}
							</span>
						))}
					</div>
				</div>
			</div>
			{service.includeNote ? (
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					{service.includeNote}
				</p>
			) : null}
			{included ? (
				<>
					<SourceSection
						service={service}
						choices={choices}
						onChoices={onChoices}
					/>
					<NetworkSection
						service={service}
						choices={choices}
						onChoices={onChoices}
					/>
					<StorageSection
						service={service}
						choices={choices}
						onChoices={onChoices}
					/>
					<RuntimeSection service={service} />
					<EnvTable service={service} choices={choices} onChoices={onChoices} />
					<NoteAlerts notes={service.notes} />
				</>
			) : null}
		</li>
	);
}
