import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
	ArrowLeftIcon,
	BookOpenIcon,
	ExternalLinkIcon,
	GitBranchIcon,
	GlobeIcon,
} from "lucide-react";
import { type ReactNode, useId, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { CatalogTemplate, TemplatePlanCheck } from "#/api/gen/types.gen";
import type { Environment, Server } from "#/api/models";
import { ErrorState } from "#/components/common/error-state";
import { DocsLink } from "#/components/docs/docs-link";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	DialogBody,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { ServerPicker } from "#/features/create/create-dialog";
import { InputsForm } from "./inputs-form";
import {
	defaultServerId,
	installInputs,
	lacksUnrestrictedServer,
	licenseClassLabel,
	licenseExplanation,
	licenseTone,
	memoryHint,
	missingInputs,
	needsUnrestricted,
	serverRestrictions,
	serviceCards,
	summarizeChecks,
	UNRESTRICTED_DOCS,
} from "./lib";
import { LicenseChip } from "./license-chip";
import { Notice, PlanChecks, SecretsNotice } from "./plan-checks";
import { planQuery, templateQuery } from "./queries";
import { ServicePlanCard } from "./service-plan-card";
import { templatesActions } from "./store";
import { TemplateIcon } from "./template-icon";
import { UnrestrictedBadge } from "./unrestricted-badge";
import { useDebounced } from "./use-debounced";
import { useInstallTemplate } from "./use-install";

const PLAN_DEBOUNCE_MS = 350;

const LINKS = [
	{ key: "website", label: "Website", Icon: GlobeIcon },
	{ key: "docs", label: "Docs", Icon: BookOpenIcon },
	{ key: "source", label: "Source", Icon: GitBranchIcon },
] as const;

function Links({ links }: { links: CatalogTemplate["links"] }) {
	const shown = LINKS.filter((link) => links[link.key]);
	if (shown.length === 0) return null;
	return (
		<div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
			{shown.map(({ key, label, Icon }) => (
				<a
					key={key}
					href={links[key]}
					target="_blank"
					rel="noreferrer noopener"
					className="inline-flex items-center gap-1.5 rounded-sm text-[12px] text-accent-ink outline-none transition-[color] duration-150 hover:text-ink-1 focus-visible:focus-ring"
				>
					<Icon aria-hidden className="size-3.5" />
					{label}
					<ExternalLinkIcon aria-hidden className="size-3 opacity-70" />
				</a>
			))}
		</div>
	);
}

function Column({ title, children }: { title: string; children: ReactNode }) {
	return (
		<section className="flex flex-col gap-2.5">
			<h3 className="type-label">{title}</h3>
			{children}
		</section>
	);
}

function DetailSkeleton() {
	return (
		<>
			<DialogHeader>
				<DialogTitle className="sr-only">Template</DialogTitle>
				<Skeleton className="h-5 w-48" />
				<Skeleton className="h-4 w-80" />
			</DialogHeader>
			<DialogBody>
				<Skeleton className="h-[220px] w-full rounded-card" />
			</DialogBody>
		</>
	);
}

/**
 * The plan step: what the template creates on the left, what it needs from
 * you on the right (inputs, server, a live preflight), and the two ways to
 * add it. Both stay disabled until a fresh preflight passes.
 */
export function DetailView({
	templateId,
	environment,
	projectId,
	servers,
}: {
	templateId: string;
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	projectId: string;
	servers: readonly Server[];
}) {
	const template = useQuery(templateQuery(templateId));
	if (template.isPending) return <DetailSkeleton />;
	if (template.isError) {
		return (
			<DialogBody className="py-8">
				<DialogTitle className="sr-only">Template unavailable</DialogTitle>
				<ErrorState
					title="That template did not load"
					body={`${errorMessage(template.error)} Nothing was changed.`}
					retry={() => void template.refetch()}
				/>
				<Button
					variant="secondary"
					className="self-center"
					onClick={() => templatesActions.select(null)}
				>
					<ArrowLeftIcon aria-hidden />
					All templates
				</Button>
			</DialogBody>
		);
	}
	return (
		<Plan
			template={template.data}
			environment={environment}
			projectId={projectId}
			servers={servers}
		/>
	);
}

function Plan({
	template,
	environment,
	projectId,
	servers,
}: {
	template: CatalogTemplate;
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	projectId: string;
	servers: readonly Server[];
}) {
	const idPrefix = useId();
	const inputs = template.inputs ?? [];
	const cards = useMemo(() => serviceCards(template), [template]);
	const usable = useMemo(
		() =>
			servers.filter(
				(server) => server.status !== "revoked" && server.status !== "pending",
			),
		[servers],
	);

	const [typed, setTyped] = useState<Record<string, string>>({});
	const [pickedServer, setPickedServer] = useState("");
	const requiresUnrestricted = needsUnrestricted(template);
	const restrictions = useMemo(
		() => serverRestrictions(usable, requiresUnrestricted),
		[usable, requiresUnrestricted],
	);
	const noUnrestricted =
		usable.length > 0 && lacksUnrestrictedServer(usable, requiresUnrestricted);
	const serverId =
		pickedServer || defaultServerId(usable, requiresUnrestricted);
	const missing = missingInputs(inputs, typed);
	const body = installInputs(inputs, typed);

	// The preflight follows the form, a beat after the last keystroke.
	const fingerprint = JSON.stringify({ serverId, inputs: body });
	const settledFingerprint = useDebounced(fingerprint, PLAN_DEBOUNCE_MS);
	const planned = useMemo(
		() =>
			JSON.parse(settledFingerprint) as {
				serverId: string;
				inputs: Record<string, string>;
			},
		[settledFingerprint],
	);
	const plan = useQuery({
		...planQuery({
			environmentId: environment.id,
			templateId: template.id,
			serverId: planned.serverId,
			inputs: planned.inputs,
		}),
		enabled: missing.length === 0,
		placeholderData: keepPreviousData,
	});

	const fresh =
		missing.length === 0 &&
		settledFingerprint === fingerprint &&
		!plan.isFetching &&
		!plan.isPlaceholderData;
	const checks: TemplatePlanCheck[] | null | undefined = plan.isError
		? [{ id: "plan", status: "fail", message: errorMessage(plan.error) }]
		: plan.data?.checks;
	const summary = useMemo(
		() => (checks ? summarizeChecks(checks) : undefined),
		[checks],
	);
	const installable =
		fresh &&
		!plan.isError &&
		plan.data?.installable === true &&
		serverId !== "";

	const { install, failure } = useInstallTemplate({
		environment,
		projectId,
		templateId: template.id,
		templateName: template.name,
		onInstalled: templatesActions.close,
	});
	const submit = (deploy: boolean) => {
		if (!installable || install.isPending) return;
		install.mutate({ serverId, inputs: body, deploy });
	};

	const license = {
		license: template.upstream.license,
		licenseClass: template.upstream.class,
		...(template.upstream.licenseUrl
			? { licenseUrl: template.upstream.licenseUrl }
			: {}),
	};
	const flagged = licenseTone(license.licenseClass) === "flagged";

	const waiting =
		missing.length > 0
			? `Fill in ${inputs
					.filter((input) => missing.includes(input.key))
					.map((input) => input.label)
					.join(", ")} to run the checks.`
			: null;
	const reason = !installable
		? noUnrestricted
			? "No unrestricted server: an instance admin turns it on in the server settings."
			: usable.length === 0
				? "No server can take this stack yet."
				: serverId === ""
					? "Choose a server."
					: (waiting ??
						(summary && summary.fail > 0
							? "Fix the failed check to continue."
							: null))
		: null;

	return (
		<>
			<DialogHeader className="gap-3">
				<div className="flex items-start gap-3">
					<Button
						variant="ghost"
						size="icon-sm"
						aria-label="Back to all templates"
						onClick={() => templatesActions.select(null)}
						className="-ml-1.5 mt-0.5"
					>
						<ArrowLeftIcon aria-hidden />
					</Button>
					<TemplateIcon icon={template.icon} name={template.name} size={40} />
					<div className="flex min-w-0 flex-1 flex-col gap-1.5">
						<div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
							<DialogTitle>{template.name}</DialogTitle>
							<LicenseChip item={license} />
							<Badge className="capitalize">{template.category}</Badge>
							{requiresUnrestricted ? <UnrestrictedBadge /> : null}
						</div>
						<DialogDescription>{template.summary}</DialogDescription>
					</div>
				</div>
			</DialogHeader>
			<DialogBody className="grid gap-6 md:min-h-0 md:grid-cols-[minmax(0,1fr)_340px] md:grid-rows-[minmax(0,1fr)] md:overflow-hidden">
				<div className="flex min-w-0 flex-col gap-5 md:min-h-0 md:overflow-y-auto md:px-1.5">
					{template.description ? (
						<p className="whitespace-pre-line text-pretty text-[13px] text-ink-2 leading-[1.6]">
							{template.description}
						</p>
					) : null}
					<Links links={template.links} />
					<Column
						title={`Creates ${cards.length} ${cards.length === 1 ? "service" : "services"}`}
					>
						<ul className="m-0 flex list-none flex-col gap-2 p-0">
							{cards.map((card) => (
								<ServicePlanCard key={card.key} card={card} />
							))}
						</ul>
						<p className="font-mono text-2xs text-ink-3 tabular-nums">
							Needs about {memoryHint(template.requires.memoryMb)} RAM
							{template.requires.diskGb > 0
								? ` and ${template.requires.diskGb} GB disk`
								: ""}
							{(template.requires.arch ?? []).length > 0
								? ` · ${(template.requires.arch ?? []).join(", ")}`
								: ""}
						</p>
					</Column>
					{(template.notes ?? []).length > 0 ? (
						<Column title="Good to know">
							<ul className="m-0 flex list-disc flex-col gap-1.5 pl-4 text-[12px] text-ink-2 leading-[1.5]">
								{(template.notes ?? []).map((note) => (
									<li key={note}>{note}</li>
								))}
							</ul>
						</Column>
					) : null}
				</div>
				<div className="flex min-w-0 flex-col gap-5 md:min-h-0 md:overflow-y-auto md:border-line md:border-l md:pr-1.5 md:pl-6">
					{inputs.length > 0 ? (
						<Column title="Configuration">
							<InputsForm
								idPrefix={idPrefix}
								inputs={inputs}
								typed={typed}
								onChange={(key, value) =>
									setTyped((current) => ({ ...current, [key]: value }))
								}
							/>
						</Column>
					) : null}
					<Column title="Server">
						{usable.length === 0 ? (
							<p className="text-[12px] text-ink-3 leading-[1.5]">
								No server is enrolled yet.{" "}
								<Link
									to="/servers"
									className="text-accent-ink underline-offset-2 hover:underline"
								>
									Add a server
								</Link>{" "}
								first.
							</p>
						) : (
							<>
								<ServerPicker
									servers={usable}
									value={serverId}
									onChange={setPickedServer}
									disabledReasons={restrictions}
								/>
								{requiresUnrestricted && !noUnrestricted ? (
									<p className="text-[12px] text-ink-3 leading-[1.5]">
										Restricted servers are disabled: this template needs host
										access.{" "}
										<DocsLink to={UNRESTRICTED_DOCS}>
											About the unrestricted switch
										</DocsLink>
									</p>
								) : null}
							</>
						)}
					</Column>
					{noUnrestricted ? (
						<Notice title="No unrestricted server">
							<p>
								This template needs a server an instance admin made
								unrestricted, and none of yours is. An instance admin can turn
								Unrestricted on in the server's settings (Servers, the server,
								Unrestricted); then come back here.
							</p>
							<DocsLink to={UNRESTRICTED_DOCS}>
								About the unrestricted switch
							</DocsLink>
						</Notice>
					) : null}
					<PlanChecks
						summary={summary}
						checking={missing.length === 0 && (!fresh || plan.isFetching)}
						waiting={waiting}
					/>
					<SecretsNotice secrets={plan.data?.secrets ?? []} />
					{plan.data?.proxyRestart ? (
						<Notice title="Opens public ports">
							This stack publishes TCP/UDP ports on the server's public
							interface. The proxy there restarts briefly (about a second) the
							first time a new port is added.
						</Notice>
					) : null}
					{flagged ? (
						<Notice title={licenseClassLabel(license.licenseClass)}>
							<p>{licenseExplanation(license)}</p>
							{license.licenseUrl ? (
								<a
									href={license.licenseUrl}
									target="_blank"
									rel="noreferrer noopener"
									className="inline-flex items-center gap-1 text-accent-ink hover:text-ink-1"
								>
									Read the {license.license} licence
									<ExternalLinkIcon aria-hidden className="size-3" />
								</a>
							) : null}
						</Notice>
					) : null}
					{failure ? (
						<Alert variant="destructive">
							<AlertTitle>{failure.title}</AlertTitle>
							<AlertDescription>
								<p>{failure.body}</p>
								{failure.replan ? (
									<Button
										variant="secondary"
										size="xs"
										onClick={() => {
											install.reset();
											void plan.refetch();
										}}
									>
										Check again
									</Button>
								) : null}
							</AlertDescription>
						</Alert>
					) : null}
				</div>
			</DialogBody>
			<DialogFooter>
				<span
					role="status"
					className="mr-auto min-w-0 text-[12px] text-ink-3 sm:max-w-[46%]"
				>
					{reason}
				</span>
				<Button
					variant={environment.isProduction ? "primary" : "secondary"}
					size="lg"
					disabled={!installable || install.isPending}
					onClick={() => submit(false)}
				>
					{install.isPending && !install.variables.deploy ? (
						<Spinner data-icon="inline-start" />
					) : null}
					Add to canvas
				</Button>
				<Button
					variant={environment.isProduction ? "secondary" : "primary"}
					size="lg"
					disabled={!installable || install.isPending}
					onClick={() => submit(true)}
				>
					{install.isPending && install.variables.deploy ? (
						<Spinner data-icon="inline-start" />
					) : null}
					Add and deploy
				</Button>
			</DialogFooter>
		</>
	);
}
