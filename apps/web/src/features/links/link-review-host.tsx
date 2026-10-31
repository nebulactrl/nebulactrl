import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ArrowRightIcon, Unlink2Icon } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { deleteLink } from "#/api/gen/sdk.gen";
import { invalidateOperations } from "#/api/invalidate";
import type { Service, ServiceLink } from "#/api/models";
import { useProjectContext } from "#/components/shell/use-project-context";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useLinks, useServices } from "#/features/canvas/data";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { ConnectPopover, type ConnectRequest } from "./connect-popover";
import { DatabaseSuggestion } from "./database-suggestion";
import { addedDatabases, directionsOf, orientLink, wireTargets } from "./lib";
import { type ConnectAnchor, linkActions, useLinkState } from "./store";

const NO_SERVICES: Service[] = [];
const NO_LINKS: ServiceLink[] = [];

function Endpoints({ from, to }: { from: Service; to: Service }) {
	return (
		<span className="flex min-w-0 items-center gap-2">
			<ServiceIcon service={from} size={22} />
			<span className="truncate">{from.name}</span>
			<ArrowRightIcon aria-hidden className="size-3.5 shrink-0 text-ink-3" />
			<ServiceIcon service={to} size={22} />
			<span className="truncate">{to.name}</span>
		</span>
	);
}

/**
 * Mounted once by the canvas screen. It owns the connect popover (a cable
 * dropped on a service, and the suggestion that follows a new database), and
 * the references sheet of an existing edge.
 */
export function LinkReviewHost() {
	const environment = useProjectContext()?.environment;
	const servicesQuery = useServices(environment?.id);
	const services = servicesQuery.data ?? NO_SERVICES;
	const links = useLinks(environment?.id).data ?? NO_LINKS;
	const environmentId = environment?.id;

	const [connect, setConnect] = useState<ConnectRequest | null>(null);
	const opened = useRef(0);
	const open = useCallback(
		(
			providerId: string,
			consumerIds: readonly string[],
			anchor: ConnectAnchor,
		) => {
			opened.current += 1;
			setConnect({ id: opened.current, providerId, consumerIds, anchor });
		},
		[],
	);
	const close = useCallback(() => setConnect(null), []);

	// The registered handler reads the latest list without re-registering.
	const servicesRef = useRef(services);
	servicesRef.current = services;
	const request = useCallback(
		(fromId: string, toId: string, at?: ConnectAnchor) => {
			const from = servicesRef.current.find((service) => service.id === fromId);
			const to = servicesRef.current.find((service) => service.id === toId);
			if (!from || !to) return;
			const [consumer, provider] =
				orientLink(from, to) === "reversed" ? [to, from] : [from, to];
			open(
				provider.id,
				[consumer.id],
				at ?? { x: window.innerWidth / 2, y: window.innerHeight / 3 },
			);
		},
		[open],
	);
	useEffect(() => linkActions.registerReviewer(request), [request]);

	// A database that appears after the environment was read offers itself to
	// the apps that have none.
	const seen = useRef<{ environmentId: string; ids: Set<string> } | null>(null);
	const [offer, setOffer] = useState<string | null>(null);
	const [dismissed, setDismissed] = useState<ReadonlySet<string>>(new Set());
	const loaded = servicesQuery.data !== undefined;
	useEffect(() => {
		if (!environmentId || !loaded) return;
		if (seen.current?.environmentId !== environmentId) {
			seen.current = {
				environmentId,
				ids: new Set(servicesRef.current.map((service) => service.id)),
			};
			setOffer(null);
			setDismissed(new Set());
			return;
		}
		const added = addedDatabases(seen.current.ids, services);
		for (const service of services) seen.current.ids.add(service.id);
		const newest = added.at(-1);
		if (newest) setOffer(newest.id);
	}, [environmentId, loaded, services]);

	const offered = offer ? services.find((s) => s.id === offer) : undefined;
	const offerTargets = useMemo(
		() => (offered ? wireTargets(services, links, offered) : []),
		[services, links, offered],
	);
	const targetServices = offerTargets
		.map((id) => services.find((service) => service.id === id))
		.filter((service): service is Service => service !== undefined);
	const connectButton = useRef<HTMLButtonElement>(null);
	const showOffer =
		offered !== undefined &&
		targetServices.length > 0 &&
		!dismissed.has(offered.id);

	return (
		<>
			{environmentId ? (
				<ConnectPopover
					request={connect}
					environmentId={environmentId}
					services={services}
					onClose={close}
				/>
			) : null}
			{showOffer && offered ? (
				<DatabaseSuggestion
					database={offered}
					targets={targetServices}
					connectRef={connectButton}
					onConnect={() => {
						if (connectButton.current) {
							open(
								offered.id,
								targetServices.map((service) => service.id),
								{ element: connectButton.current },
							);
						}
					}}
					onDismiss={() =>
						setDismissed((current) => new Set(current).add(offered.id))
					}
				/>
			) : null}
			<ReferencesDialog services={services} links={links} />
		</>
	);
}

/**
 * What an edge is made of: per direction, the variables of the consumer
 * that reference the provider. A manual link can be removed; one that only
 * exists because of references goes away when they do.
 */
function ReferencesDialog({
	services,
	links,
}: {
	services: readonly Service[];
	links: readonly ServiceLink[];
}) {
	const { references } = useLinkState();
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const [removing, setRemoving] = useState(false);

	// The link as the canvas has it now, so a removed one closes the sheet.
	const link = references
		? (links.find((candidate) => candidate.id === references.id) ?? null)
		: null;
	const lastLink = useRef<ServiceLink | null>(null);
	if (link) lastLink.current = link;
	const shown = link ?? lastLink.current;
	const directions = shown ? directionsOf(shown, links) : [];
	const byId = (id: string) => services.find((service) => service.id === id);
	const from = shown ? byId(shown.fromServiceId) : undefined;
	const to = shown ? byId(shown.toServiceId) : undefined;

	useEffect(() => {
		if (references && !link) linkActions.closeReferences();
	}, [references, link]);

	const openVariables = (serviceId: string) => {
		linkActions.closeReferences();
		void navigate({
			to: ".",
			search: (previous) => ({
				...previous,
				service: serviceId,
				tab: "variables",
			}),
		});
	};

	const removeManual = async (target: ServiceLink) => {
		setRemoving(true);
		try {
			await deleteLink({ path: { linkId: target.id } });
			await invalidateOperations(queryClient, ["listLinks"]);
			toast.success(
				"Link removed",
				"The services and their variables are unchanged.",
			);
			linkActions.closeReferences();
		} catch (failure) {
			toast.error("Could not remove the link", errorMessage(failure));
		} finally {
			setRemoving(false);
		}
	};

	return (
		<Dialog
			open={references !== null}
			onOpenChange={(open) => !open && linkActions.closeReferences()}
		>
			<DialogContent width={520}>
				{shown && from && to ? (
					<>
						<DialogHeader>
							<DialogTitle>
								<Endpoints from={from} to={to} />
							</DialogTitle>
							<DialogDescription>
								{from.name} is the consumer: the variables below hold the
								address of {to.name}.
							</DialogDescription>
						</DialogHeader>
						<DialogBody>
							{directions.map((direction) => {
								const consumer = byId(direction.fromServiceId);
								const provider = byId(direction.toServiceId);
								if (!consumer || !provider) return null;
								const keys = direction.references ?? [];
								return (
									<section
										key={direction.id}
										aria-label={`${consumer.name} to ${provider.name}`}
										className="flex flex-col gap-2"
									>
										<div className="flex items-center justify-between gap-3">
											<h3 className="type-eyebrow">
												{consumer.name} → {provider.name}
											</h3>
											<Button
												type="button"
												variant="link"
												onClick={() => openVariables(consumer.id)}
											>
												Open {consumer.name} variables
											</Button>
										</div>
										{keys.length === 0 ? (
											<p className="text-[13px] text-ink-3">
												No variable of {consumer.name} references{" "}
												{provider.name} yet. The link is drawn on the canvas
												only.
											</p>
										) : (
											<ul
												aria-label={`Variables of ${consumer.name} that reference ${provider.name}`}
												className="surface-well flex flex-wrap gap-1.5 rounded-ctl border border-line-control p-2"
											>
												{keys.map((reference) => (
													<li
														key={reference.key}
														className="rounded-md bg-white/6 px-2 py-1 font-mono text-xs text-ink-1"
													>
														{reference.key}
													</li>
												))}
											</ul>
										)}
										{direction.source === "reference" ? (
											<p className="text-2xs text-ink-3">
												This connection is drawn from those references. It
												disappears when {consumer.name}’s variables stop
												referencing {provider.name}.
											</p>
										) : keys.length > 0 ? (
											<p className="text-2xs text-ink-3">
												A manual link. Removing it keeps the connection drawn
												while those variables reference {provider.name}.
											</p>
										) : null}
										{direction.source === "manual" ? (
											<div>
												<Button
													type="button"
													variant="danger"
													size="sm"
													disabled={removing}
													onClick={() => void removeManual(direction)}
												>
													{removing ? <Spinner /> : <Unlink2Icon aria-hidden />}
													Remove manual link
												</Button>
											</div>
										) : null}
									</section>
								);
							})}
						</DialogBody>
						<DialogFooter>
							<Button
								type="button"
								variant="secondary"
								onClick={() => linkActions.closeReferences()}
							>
								Close
							</Button>
						</DialogFooter>
					</>
				) : null}
			</DialogContent>
		</Dialog>
	);
}
