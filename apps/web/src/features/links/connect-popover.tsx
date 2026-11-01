import { useQueries, useQueryClient } from "@tanstack/react-query";
import { ArrowRightIcon, LockIcon } from "lucide-react";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { previewLinkOptions } from "#/api/gen/@tanstack/react-query.gen";
import { createLink, putVariables } from "#/api/gen/sdk.gen";
import type { LinkPreview } from "#/api/gen/types.gen";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { Checkbox } from "#/components/ui/checkbox";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Kbd } from "#/components/ui/kbd";
import { Popover, PopoverContent } from "#/components/ui/popover";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { openStagedReview } from "#/features/deploy-button";
import {
	isBlocked,
	mergePlans,
	needsAcceptance,
	plural,
	toMeshPlan,
} from "#/features/mesh/model";
import { MeshPlanBlock } from "#/features/mesh/plan-block";
import {
	chosenVariables,
	type ProposalRow,
	rowStates,
	rowsFromProposals,
} from "./lib";
import type { ConnectAnchor } from "./store";

/** One opening of the popover: who reads, what they read from, and where it opens. */
export interface ConnectRequest {
	/** Changes on every opening, so a new request starts from a clean form. */
	id: number;
	providerId: string;
	consumerIds: readonly string[];
	anchor: ConnectAnchor;
}

interface Props {
	request: ConnectRequest | null;
	environmentId: string;
	services: readonly Service[];
	onClose(): void;
}

/** A zero-size anchor at a screen point, which is what a dropped cable leaves. */
function virtualAnchor(at: { x: number; y: number }) {
	return {
		getBoundingClientRect: () =>
			DOMRect.fromRect({ x: at.x, y: at.y, width: 0, height: 0 }),
	};
}

function anchorOf(anchor: ConnectAnchor) {
	return "element" in anchor ? anchor.element : virtualAnchor(anchor);
}

/**
 * What a cable drawn between two services means, asked where it was dropped:
 * the references the consumer could hold to reach the provider, the
 * recommended ones ticked with names that can be edited. Confirming stages
 * them on the consumer (they apply on its next deploy) and the cable is drawn
 * from them; Esc leaves everything as it was. There is no modal and nothing is
 * created before the reader says so.
 *
 * The same popover serves the suggestion that follows a new database, which
 * opens it on the "Connect" button with every app that has no database yet.
 */
export function ConnectPopover({
	request,
	environmentId,
	services,
	onClose,
}: Props) {
	// Keep the last request while the popup animates closed.
	const last = useRef<ConnectRequest | null>(null);
	if (request !== null) last.current = request;
	const shown = request ?? last.current;
	return (
		<Popover
			open={request !== null}
			onOpenChange={(open) => !open && onClose()}
		>
			{shown ? (
				<PopoverContent
					anchor={anchorOf(shown.anchor)}
					side={"element" in shown.anchor ? "top" : "bottom"}
					align="start"
					sideOffset={12}
					aria-label="Connect services"
					className="max-h-[min(72vh,520px)] w-[348px] gap-0 overflow-y-auto overscroll-contain p-0"
				>
					{request ? (
						<ConnectBody
							key={request.id}
							request={request}
							environmentId={environmentId}
							services={services}
							onClose={onClose}
						/>
					) : null}
				</PopoverContent>
			) : null}
		</Popover>
	);
}

function Endpoints({
	consumers,
	provider,
}: {
	consumers: readonly Service[];
	provider: Service;
}) {
	return (
		<span className="flex min-w-0 items-center gap-1.5 font-semibold text-[13px] text-ink-1">
			{consumers.length === 1 && consumers[0] ? (
				<>
					<ServiceIcon service={consumers[0]} size={18} />
					<span className="truncate">{consumers[0].name}</span>
				</>
			) : (
				<span className="truncate">{consumers.length} services</span>
			)}
			<ArrowRightIcon aria-hidden className="size-3.5 shrink-0 text-ink-3" />
			<ServiceIcon service={provider} size={18} />
			<span className="truncate">{provider.name}</span>
		</span>
	);
}

type Rows = Record<string, ProposalRow[]>;

function ConnectBody({
	request,
	environmentId,
	services,
	onClose,
}: {
	request: ConnectRequest;
	environmentId: string;
	services: readonly Service[];
	onClose(): void;
}) {
	const queryClient = useQueryClient();
	const provider = services.find((s) => s.id === request.providerId);
	const consumers = useMemo(
		() =>
			request.consumerIds
				.map((id) => services.find((service) => service.id === id))
				.filter((service): service is Service => service !== undefined),
		[request.consumerIds, services],
	);

	const previews = useQueries({
		queries: consumers.map((consumer) => ({
			...previewLinkOptions({
				path: { environmentId },
				query: { from: consumer.id, to: request.providerId },
			}),
			// What the consumer holds changes under us; never show an old answer.
			staleTime: 0,
			gcTime: 0,
			retry: false,
		})),
	});
	const [rows, setRows] = useState<Rows>({});
	const [more, setMore] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [acceptMesh, setAcceptMesh] = useState(false);

	// A preview becomes editable rows once, when it first arrives.
	useEffect(() => {
		setRows((current) => {
			let next: Rows | null = null;
			previews.forEach((preview, index) => {
				const consumer = consumers[index];
				if (!consumer || !preview.data || current[consumer.id]) return;
				next ??= { ...current };
				next[consumer.id] = rowsFromProposals(
					preview.data.proposedVariables ?? [],
				);
			});
			return next ?? current;
		});
	}, [previews, consumers]);

	if (!provider || consumers.length === 0) return null;
	const loading = previews.some((preview) => preview.isPending);
	const failed = previews.find((preview) => preview.isError);
	const previewOf = (consumerId: string): LinkPreview | undefined =>
		previews[consumers.findIndex((c) => c.id === consumerId)]?.data;

	const perConsumer = consumers.map((consumer) => {
		const list = rows[consumer.id] ?? [];
		const preview = previewOf(consumer.id);
		const states = rowStates(list, new Set(preview?.existingKeys ?? []));
		return { consumer, list, preview, states };
	});
	const meshPlan = mergePlans(
		perConsumer.map(({ preview }) => toMeshPlan(preview?.mesh)),
	);
	const blocked =
		isBlocked(meshPlan) ||
		perConsumer.some(({ states }) =>
			states.some((state) => state.error !== undefined),
		);
	const total = perConsumer.reduce(
		(sum, { list }) => sum + chosenVariables(list).length,
		0,
	);
	const warnings = [
		...new Set(perConsumer.flatMap(({ preview }) => preview?.warnings ?? [])),
	];
	const meshNeedsAccept =
		needsAcceptance(meshPlan) && !acceptMesh && !isBlocked(meshPlan);
	const meshAccepted = needsAcceptance(meshPlan) && acceptMesh;
	const single = consumers.length === 1;
	const linkedAlready = perConsumer.every(({ preview }) => preview?.linked);

	const setRow = (
		consumerId: string,
		index: number,
		patch: Partial<ProposalRow>,
	) =>
		setRows((current) => ({
			...current,
			[consumerId]: (current[consumerId] ?? []).map((row, at) =>
				at === index ? { ...row, ...patch } : row,
			),
		}));

	const confirm = async (event?: FormEvent) => {
		event?.preventDefault();
		if (busy || blocked || loading) return;
		setBusy(true);
		setError(null);
		try {
			for (const { consumer, list } of perConsumer) {
				const chosen = chosenVariables(list);
				if (chosen.length > 0) {
					await putVariables({
						path: { serviceId: consumer.id },
						body: {
							variables: chosen.map((variable) => ({
								key: variable.key,
								value: variable.value,
								isSecret: false,
							})),
						},
					});
				}
			}
			if (total === 0 || meshAccepted) {
				// No variables: draw the cable by hand, as the review always could.
				// Over the mesh the cable also asks for its link, which the person
				// has just accepted.
				for (const { consumer, preview } of perConsumer) {
					if (preview?.linked) continue;
					await createLink({
						path: { environmentId },
						body: {
							fromServiceId: consumer.id,
							toServiceId: provider.id,
							...(meshAccepted ? { acceptMeshChanges: true } : {}),
						},
					});
				}
			}
			await Promise.all([
				invalidateServices(queryClient),
				invalidateOperations(queryClient, ["listLinks"]),
			]);
			toast.add({
				type: "success",
				title:
					total > 0
						? `Staged ${total} variable${total === 1 ? "" : "s"} for ${provider.name}`
						: `Linked to ${provider.name}`,
				description:
					total > 0
						? `${single ? (consumers[0]?.name ?? "The service") : "The services"} read ${provider.name} after the next deploy.`
						: "The cable is drawn; no variable was added.",
				timeout: 6000,
				...(total > 0
					? { actionProps: { children: "Review", onClick: openStagedReview } }
					: {}),
			});
			onClose();
		} catch (failure) {
			setError(errorMessage(failure));
			// Whatever was written is real: show it.
			void invalidateServices(queryClient);
		} finally {
			setBusy(false);
		}
	};

	const label =
		total > 0
			? `Stage ${total} variable${total === 1 ? "" : "s"}`
			: linkedAlready
				? "Nothing to stage"
				: meshPlan
					? "Link over the mesh"
					: "Link only";

	return (
		<form onSubmit={confirm} className="flex flex-col">
			<div className="flex flex-col gap-1 px-3.5 pt-3 pb-2.5">
				<Endpoints consumers={consumers} provider={provider} />
				<p className="text-2xs text-ink-3 leading-[1.5]">
					{single
						? `Stages these variables on ${consumers[0]?.name}. They apply on its next deploy.`
						: `${provider.name} is not wired into these services yet. Stage the references on each; they apply on its next deploy.`}
				</p>
			</div>
			<div className="flex flex-col gap-3 border-line-faint border-t px-3.5 py-3">
				{warnings.map((warning) => (
					<Alert key={warning} variant="warning">
						<AlertDescription>{warning}</AlertDescription>
					</Alert>
				))}
				{meshPlan ? (
					<div className="flex flex-col gap-2">
						<MeshPlanBlock mesh={meshPlan} />
						{needsAcceptance(meshPlan) && !isBlocked(meshPlan) ? (
							<CheckLabel
								checked={acceptMesh}
								onCheckedChange={setAcceptMesh}
								labelClassName="text-2xs text-ink-2"
							>
								Connect over the mesh: adds{" "}
								{plural(meshPlan.devicesAdded, "device")}
							</CheckLabel>
						) : null}
					</div>
				) : null}
				{loading ? (
					<div aria-busy="true" className="flex flex-col gap-2">
						<Skeleton className="h-7 w-full" />
						<Skeleton className="h-7 w-full" />
						<Skeleton className="h-7 w-3/4" />
					</div>
				) : failed ? (
					<FieldError>{errorMessage(failed.error)}</FieldError>
				) : (
					perConsumer.map(({ consumer, list, states, preview }) => {
						const visible = list
							.map((row, index) => ({ row, index }))
							.filter(
								({ row }) => row.recommended === true || more || row.checked,
							);
						const hidden = list.length - visible.length;
						return (
							<section
								key={consumer.id}
								aria-label={`Variables for ${consumer.name}`}
								className="flex flex-col gap-1.5"
							>
								{single ? null : (
									<h3 className="flex items-center gap-1.5 font-medium text-ink-1 text-xs">
										<ServiceIcon service={consumer} size={16} />
										{consumer.name}
									</h3>
								)}
								{preview?.linked && single ? (
									<p className="text-2xs text-ink-3">
										Already connected. Add more references below.
									</p>
								) : null}
								<ul className="flex flex-col gap-2">
									{visible.map(({ row, index }) => {
										const state = states[index];
										return (
											<li
												key={row.proposed}
												className="flex items-start gap-2.5"
											>
												<Checkbox
													checked={row.checked}
													aria-label={`Stage ${row.key || row.proposed} on ${consumer.name}`}
													className="mt-1.5"
													onCheckedChange={(checked) =>
														setRow(consumer.id, index, {
															checked: checked === true,
														})
													}
												/>
												<div className="min-w-0 flex-1">
													<Input
														aria-label={`Name of ${row.proposed} on ${consumer.name}`}
														aria-invalid={state?.error !== undefined}
														autoComplete="off"
														spellCheck={false}
														disabled={!row.checked}
														className="h-7 font-mono text-xs"
														value={row.key}
														onChange={(event) =>
															setRow(consumer.id, index, {
																key: event.target.value,
															})
														}
													/>
													<p
														title={row.description}
														className="mt-1 flex min-w-0 items-center gap-1 font-mono text-2xs text-ink-3"
													>
														{row.secret ? (
															<>
																<LockIcon
																	aria-hidden
																	className="size-2.5 shrink-0"
																/>
																<span className="sr-only">
																	Resolves to a secret.{" "}
																</span>
															</>
														) : null}
														<span className="truncate">{row.value}</span>
													</p>
													{state?.error ? (
														<FieldError>{state.error}</FieldError>
													) : row.status === "unchanged" &&
														state?.replaces === false ? (
														<p className="mt-0.5 text-2xs text-ink-3">
															Already set.
														</p>
													) : row.checked && state?.replaces ? (
														<p className="mt-0.5 text-2xs text-warn">
															{consumer.name} already has {row.key}; this
															replaces it.
														</p>
													) : null}
												</div>
											</li>
										);
									})}
								</ul>
								{hidden > 0 ? (
									<Button
										type="button"
										variant="link"
										className="self-start"
										onClick={() => setMore(true)}
									>
										{hidden} more reference{hidden === 1 ? "" : "s"}
									</Button>
								) : null}
							</section>
						);
					})
				)}
				{error ? <FieldError>{error}</FieldError> : null}
			</div>
			<div className="flex items-center justify-between gap-2 border-line-faint border-t px-3.5 py-2.5">
				<span className="flex items-center gap-1 text-2xs text-ink-3">
					<Kbd>esc</Kbd> cancel
				</span>
				<div className="flex items-center gap-2">
					<Button
						type="button"
						variant="ghost"
						size="sm"
						disabled={busy}
						onClick={onClose}
					>
						Cancel
					</Button>
					<Button
						type="submit"
						size="sm"
						disabled={
							busy ||
							blocked ||
							loading ||
							failed !== undefined ||
							(total === 0 && linkedAlready) ||
							(total === 0 && meshNeedsAccept)
						}
					>
						{busy ? <Spinner /> : null}
						{label}
					</Button>
				</div>
			</div>
		</form>
	);
}
