import { useMutation, useQueryClient } from "@tanstack/react-query";
import { WandSparklesIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { generateDomain } from "#/api/gen/sdk.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { PublicPortSection } from "#/features/databases/public-port-section";
import { isManagedDatabase } from "#/features/databases/queries";
import { ServiceNetworks } from "#/features/networks/service-networks";
import { effectiveProcess } from "#/features/processes/domain-process-target";
import {
	defaultProcessName,
	domainProcessOptions,
	hasProcesses,
	storedDomainProcess,
} from "#/features/processes/model";
import { PortExposures } from "#/features/proxy/port-exposures";
import { AddDomainForm, DomainList } from "./domain-editor/domain-list";
import {
	type DraftProps,
	Section,
	SectionBody,
	SettingRow,
	TextRow,
} from "./fields";
import { OUTBOUND_HELP } from "./outbound-copy";

/**
 * Port and public domains. Each domain is an editor card (host, path, redirect,
 * access, headers, limits, TLS); every domain change is staged like any other.
 * Below them, the TCP/UDP ports that expose this service.
 */
export function NetworkingSection({
	service,
	draft,
	errors,
	set,
}: DraftProps & { service: Service }) {
	const queryClient = useQueryClient();
	const processed = hasProcesses(service);
	const processOptions = domainProcessOptions(service);
	// The process a new or generated domain serves; shared with the add form below.
	const [pickedProcess, setPickedProcess] = useState("");
	const serving = effectiveProcess(processOptions, pickedProcess);
	const hasPort = processed
		? processOptions.length > 0
		: service.config.port !== undefined && service.config.port > 0;
	const name = defaultProcessName({ processName: draft.processName.trim() });
	const otherPorts = draft.processes.some((p) => p.port.trim() !== "");

	const generate = useMutation({
		mutationFn: async () => {
			await generateDomain({
				path: { serviceId: service.id },
				...(processed
					? { query: { process: storedDomainProcess(service, serving) } }
					: {}),
			});
		},
		onSuccess: async () => {
			await invalidateServices(queryClient);
			toast.success("Domain generated", "Staged until the next deploy.");
		},
	});

	return (
		<Section
			title="Networking"
			description="How people and other services reach this service."
		>
			<TextRow
				id="set-port"
				field="config.port"
				label={processed ? `Port of ${name}` : "Port"}
				description={
					processed
						? `The port the default process listens on. Empty means no HTTP.${otherPorts ? " Each process needs its own port." : ""}`
						: "The port the app listens on. Empty means no HTTP."
				}
				type="number"
				narrow
				value={draft.port}
				error={errors.port}
				placeholder="none"
				onChange={(value) => set("port", value)}
			/>
			<SectionBody anchor="domains">
				<div className="mb-3 flex items-center gap-2">
					<span className="font-medium text-ink-1 text-sm">Domains</span>
					<Button
						variant="secondary"
						size="xs"
						className="ml-auto"
						disabled={
							!hasPort || generate.isPending || (processed && serving === "")
						}
						title={
							hasPort
								? processed && serving === ""
									? "Choose which process serves the domain"
									: "Generate a hostname from the server's base domain"
								: processed
									? "Give a process a port first"
									: "Set a port first"
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
				{service.domains.length === 0 ? (
					<p className="type-secondary mb-3">
						{hasPort
							? "No domain yet. Add one, or generate one from the server's base domain."
							: "A service needs a port before it can have a domain."}
					</p>
				) : (
					<div className="mb-3">
						<DomainList service={service} />
					</div>
				)}
				<AddDomainForm
					service={service}
					disabled={!hasPort}
					disabledReason={
						processed ? "Give a process a port first" : "Set a port first"
					}
					process={serving}
					onProcessChange={setPickedProcess}
				/>
				{generate.error ? (
					<FieldError className="mt-2">
						{errorMessage(generate.error)}
					</FieldError>
				) : null}
			</SectionBody>
			<SectionBody anchor="port-exposures">
				<PortExposures service={service} />
			</SectionBody>
			{isManagedDatabase(service) ? (
				<SectionBody anchor="public-port">
					<PublicPortSection service={service} />
				</SectionBody>
			) : null}
			{service.kind === "route" ? null : (
				<>
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
				</>
			)}
		</Section>
	);
}
