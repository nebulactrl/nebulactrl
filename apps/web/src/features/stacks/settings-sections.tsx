import { useMutation, useQueryClient } from "@tanstack/react-query";
import { WandSparklesIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { generateDomain } from "#/api/gen/sdk.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { DocsLink } from "#/components/docs/docs-link";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { ServiceNetworks } from "#/features/networks/service-networks";
import {
	AddDomainForm,
	DomainList,
} from "#/features/service-settings/domain-editor/domain-list";
import {
	type DraftProps,
	Section,
	SectionBody,
	SettingRow,
	TextRow,
} from "#/features/service-settings/fields";
import { OUTBOUND_HELP } from "#/features/service-settings/outbound-copy";
import { ComposeEditor } from "./compose-editor";
import {
	alignResources,
	resourceErrors,
	type StackCheckState,
	type StackComponent,
} from "./model";

/** The compose file with its live check; the control plane derives the components from it on save. */
export function ComposeSection({
	draft,
	errors,
	set,
	check,
	serverName,
}: DraftProps & {
	check: StackCheckState;
	serverName: string | undefined;
}) {
	return (
		<Section
			title="Compose file"
			description={
				<>
					The services of this file are the stack's components.{" "}
					<code className="font-mono">
						{"$"}
						{"{VAR}"}
					</code>{" "}
					placeholders read the service's variables. Saving stages the change;
					the next deploy runs it. <DocsLink to="operations/compose-stacks" />
				</>
			}
		>
			<SectionBody anchor="compose">
				<ComposeEditor
					id="set-compose"
					label="docker-compose.yml"
					value={draft.stackCompose}
					onChange={(next) => set("stackCompose", next)}
					state={check}
					serverName={serverName}
					error={errors.stackCompose}
					minRows={18}
				/>
			</SectionBody>
		</Section>
	);
}

/** How long a deploy waits for the components, and how long they get to stop. */
export function StackDeploySection({ draft, errors, set }: DraftProps) {
	return (
		<Section
			title="Deploy"
			description="Compose stacks are always recreated: docker compose up replaces the containers, so components restart on every deploy."
		>
			<TextRow
				id="set-stack-wait"
				label="Wait timeout"
				description="Seconds a deploy waits for every component to become healthy. Empty is 300."
				type="number"
				narrow
				value={draft.stackWaitSeconds}
				error={errors.stackWaitSeconds}
				placeholder="300"
				onChange={(value) => set("stackWaitSeconds", value)}
			/>
			<TextRow
				id="set-stop-grace"
				label="Stop grace period"
				description="Seconds a component gets to stop before it is killed."
				type="number"
				narrow
				value={draft.stopGraceSeconds}
				error={errors.stopGraceSeconds}
				onChange={(value) => set("stopGraceSeconds", value)}
			/>
		</Section>
	);
}

function LimitFields({
	component,
	value,
	error,
	onChange,
}: {
	component: StackComponent;
	value: { memoryMb: string; cpus: string };
	error: { memoryMb?: string; cpus?: string } | undefined;
	onChange(next: { memoryMb: string; cpus: string }): void;
}) {
	const id = `limits-${component.name}`;
	return (
		<SettingRow
			label={<span className="font-mono">{component.name}</span>}
			description={
				component.image ? (
					<span className="font-mono">{component.image}</span>
				) : undefined
			}
		>
			<div className="flex flex-wrap items-start gap-3">
				<div className="flex flex-col gap-1">
					<label htmlFor={`${id}-memory`} className="text-2xs text-ink-3">
						Memory (MB)
					</label>
					<Input
						id={`${id}-memory`}
						inputMode="numeric"
						autoComplete="off"
						className="w-[130px] font-mono"
						placeholder="no limit"
						value={value.memoryMb}
						aria-invalid={error?.memoryMb !== undefined}
						onChange={(event) =>
							onChange({ ...value, memoryMb: event.target.value })
						}
					/>
					<FieldError>{error?.memoryMb}</FieldError>
				</div>
				<div className="flex flex-col gap-1">
					<label htmlFor={`${id}-cpus`} className="text-2xs text-ink-3">
						CPUs
					</label>
					<Input
						id={`${id}-cpus`}
						inputMode="decimal"
						autoComplete="off"
						className="w-[110px] font-mono"
						placeholder="no limit"
						value={value.cpus}
						aria-invalid={error?.cpus !== undefined}
						onChange={(event) =>
							onChange({ ...value, cpus: event.target.value })
						}
					/>
					<FieldError>{error?.cpus}</FieldError>
				</div>
			</div>
		</SettingRow>
	);
}

/**
 * Per-component memory and CPU limits. A limit the compose file sets itself
 * (`deploy.resources.limits`) wins over these. The rows follow the compose
 * text in the editor, so a new service appears here before it is saved.
 */
export function StackLimitsSection({
	service,
	draft,
	set,
	components,
}: DraftProps & {
	service: Service;
	components: readonly StackComponent[];
}) {
	const problems = resourceErrors(draft.stackResources);
	// A component the file no longer declares would be refused on save: drop its limits.
	useEffect(() => {
		const declared = new Set(components.map((c) => c.name));
		const stale = Object.keys(draft.stackResources).filter(
			(name) => !declared.has(name),
		);
		if (components.length > 0 && stale.length > 0) {
			set("stackResources", alignResources(draft.stackResources, components));
		}
	}, [components, draft.stackResources, set]);
	return (
		<Section
			title="Component limits"
			description="Memory and CPU per component, applied when the compose file sets none of its own."
		>
			<SectionBody anchor="stack-limits">
				{components.length === 0 ? (
					<p className="type-secondary py-1.5">
						{service.name} has no components yet. Add services to the Compose
						file first.
					</p>
				) : (
					components.map((component) => (
						<LimitFields
							key={component.name}
							component={component}
							value={
								draft.stackResources[component.name] ?? {
									memoryMb: "",
									cpus: "",
								}
							}
							error={problems[component.name]}
							onChange={(next) =>
								set("stackResources", {
									...draft.stackResources,
									[component.name]: next,
								})
							}
						/>
					))
				)}
			</SectionBody>
		</Section>
	);
}

/**
 * Networking of a stack: its domains (each routes one component's port),
 * outbound internet and the shared networks. Ports are never published on the
 * host; only HTTP domains reach a component.
 */
export function StackNetworkingSection({
	service,
	draft,
	set,
	components,
}: DraftProps & {
	service: Service;
	components: readonly StackComponent[];
}) {
	const queryClient = useQueryClient();
	const [component, setComponent] = useState("");
	const chosen =
		component !== ""
			? component
			: components.length === 1
				? (components[0]?.name ?? "")
				: "";
	const generate = useMutation({
		mutationFn: async () => {
			await generateDomain({
				path: { serviceId: service.id },
				query: { component: chosen },
			});
		},
		onSuccess: async () => {
			await invalidateServices(queryClient);
			toast.success("Domain generated", "Staged until the next deploy.");
		},
	});
	const noComponents = components.length === 0;

	return (
		<Section
			title="Networking"
			description="Components reach each other by name on the environment network. Ports are not published on the host: a domain routes to one component's port through the proxy."
		>
			<SectionBody anchor="domains">
				<div className="mb-3 flex flex-wrap items-center gap-2">
					<span className="font-medium text-ink-1 text-sm">Domains</span>
					<div className="ml-auto flex items-center gap-2">
						<Select
							value={chosen === "" ? null : chosen}
							items={components.map((c) => ({ value: c.name, label: c.name }))}
							disabled={noComponents}
							onValueChange={(next) => {
								if (next !== null) setComponent(next);
							}}
						>
							<SelectTrigger
								aria-label="Component to generate a domain for"
								size="sm"
								className="w-[150px] font-mono"
							>
								<SelectValue placeholder="Component" />
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false}>
								{components.map((c) => (
									<SelectItem key={c.name} value={c.name} className="font-mono">
										{c.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Button
							variant="secondary"
							size="xs"
							disabled={chosen === "" || generate.isPending}
							title={
								chosen === ""
									? "Pick the component the domain serves"
									: "Generate a hostname from the server's base domain"
							}
							onClick={() => generate.mutate()}
						>
							{generate.isPending ? (
								<Spinner />
							) : (
								<WandSparklesIcon aria-hidden />
							)}
							Generate domain
						</Button>
					</div>
				</div>
				{service.domains.length === 0 ? (
					<p className="type-secondary mb-3">
						{noComponents
							? "A stack needs components before it can have a domain."
							: "No domain yet. Add one for a component, or generate one from the server's base domain."}
					</p>
				) : (
					<div className="mb-3">
						<DomainList service={service} />
					</div>
				)}
				<AddDomainForm
					service={service}
					disabled={noComponents}
					disabledReason="The stack has no components yet"
				/>
				{generate.error ? (
					<FieldError className="mt-2">
						{errorMessage(generate.error)}
					</FieldError>
				) : null}
			</SectionBody>
			<SettingRow
				label="Block outbound internet"
				htmlFor="set-block-egress"
				description={OUTBOUND_HELP.description}
				info={OUTBOUND_HELP.info}
			>
				<div className="flex items-center gap-3">
					<Switch
						id="set-block-egress"
						checked={draft.blockEgress}
						disabled={!draft.blockEgress && service.networks.length > 0}
						onCheckedChange={(checked) => set("blockEgress", checked)}
					/>
					<span className="text-[13px] text-ink-2">
						{draft.blockEgress ? OUTBOUND_HELP.on : OUTBOUND_HELP.off}
					</span>
				</div>
				{!draft.blockEgress && service.networks.length > 0 ? (
					<p className="type-secondary">{OUTBOUND_HELP.needsDetach}</p>
				) : null}
			</SettingRow>
			<SectionBody anchor="networks">
				<ServiceNetworks service={service} />
			</SectionBody>
		</Section>
	);
}
