import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ClipboardCopyIcon, EyeIcon } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import {
	disableDatabasePublicPortMutation,
	getDatabasePublicPortOptions,
	getDatabasePublicPortQueryKey,
	setDatabasePublicPortMutation,
} from "#/api/gen/@tanstack/react-query.gen";
import type { PublicPortView } from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { CopyButton } from "#/components/common/copy-button";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { AccessRulesEditor } from "#/features/access-rules/access-rules-editor";
import { openStagedReview } from "#/features/deploy-button";
import { invalidateRoutes } from "#/features/proxy/queries";
import { RevealedValue } from "./connection-string";
import {
	connectionAddress,
	isPublicPortDirty,
	NO_PUBLIC_ADDRESS,
	needsOpenConfirmation,
	PLAINTEXT_NOTE,
	type PublicPortField,
	type PublicPortForm,
	parsePublicPort,
	publicPortError,
	publicPortStatus,
	RECREATE_PROXY_NOTE,
	toPublicPortForm,
	toSetRequest,
	validatePublicPort,
	willRecreateProxy,
} from "./public-port";
import { usePublicReveal } from "./use-public-reveal";

/** How often the view is refetched while the route waits for a deploy. */
const PUBLIC_PORT_POLL_MS = 4_000;

/**
 * The external connection string of an enabled public port: the address
 * (not a secret) and the full string with the password, revealed on demand.
 * The reveal is audited by the control plane and shown for 60 s only.
 */
function PublicConnection({
	serviceId,
	view,
}: {
	serviceId: string;
	view: PublicPortView;
}) {
	const address = connectionAddress(view);
	const { shown, mode, busy, problem, run, hide } = usePublicReveal(serviceId);
	return (
		<div className="flex flex-col gap-2">
			<span className="type-label">External connection</span>
			{address ? (
				<div className="flex items-center gap-2">
					<code className="surface-well min-w-0 flex-1 truncate rounded-ctl px-2.5 py-1.5 font-mono text-ink-1 text-xs">
						{address}
					</code>
					<CopyButton
						value={address}
						label="Copy the address"
						variant="ghost"
						size="icon-sm"
					/>
				</div>
			) : (
				<Alert variant="warning">
					<AlertDescription>{NO_PUBLIC_ADDRESS}</AlertDescription>
				</Alert>
			)}
			<div className="flex flex-wrap items-center gap-2">
				<Button
					variant="secondary"
					size="md"
					disabled={busy || !address}
					onClick={() => void run("copy")}
				>
					{mode === "copy" ? <Spinner /> : <ClipboardCopyIcon aria-hidden />}
					Copy connection string
				</Button>
				{shown === null ? (
					<Button
						variant="ghost"
						size="md"
						disabled={busy || !address}
						onClick={() => void run("show")}
					>
						{mode === "show" ? <Spinner /> : <EyeIcon aria-hidden />}
						Reveal for 60 s
					</Button>
				) : null}
			</div>
			{shown ? <RevealedValue reveal={shown} onHide={hide} /> : null}
			{problem ? <FieldError>{problem}</FieldError> : null}
			{address && !view.deployed ? (
				<p className="text-2xs text-ink-3">
					Nothing listens on this port until the route is deployed.
				</p>
			) : null}
		</div>
	);
}

function PublicPortBody({
	service,
	view,
}: {
	service: Service;
	view: PublicPortView;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const ids = useId();
	const serviceId = service.id;
	// `null` means untouched: the form follows the view as it refetches.
	const [edited, setEdited] = useState<PublicPortForm | null>(null);
	const [opening, setOpening] = useState(false);
	const [refusal, setRefusal] = useState<{
		field: PublicPortField;
		message: string;
	} | null>(null);

	const base = useMemo(() => toPublicPortForm(view), [view]);
	const form = edited ?? base;
	const set = (patch: Partial<PublicPortForm>) => {
		setEdited({ ...form, ...patch });
		setRefusal(null);
	};
	const editing = view.enabled || opening;
	const port = parsePublicPort(form.port);
	const problems = useMemo(
		() => validatePublicPort(form, service.slug),
		[form, service.slug],
	);
	const dirty = isPublicPortDirty(view, form);
	const recreate = editing && willRecreateProxy(view, port);
	const status = publicPortStatus(view);
	const open = needsOpenConfirmation(form.allow);
	const warnings = view.warnings ?? [];
	const routeId = view.routeId;

	const done = async (next: PublicPortView) => {
		queryClient.setQueryData(
			getDatabasePublicPortQueryKey({ path: { serviceId } }),
			next,
		);
		setEdited(null);
		setOpening(false);
		setRefusal(null);
		await Promise.all([
			invalidateRoutes(queryClient),
			invalidateOperations(queryClient, ["getDatabasePublicPort"]),
		]);
	};

	const save = useMutation({
		...setDatabasePublicPortMutation(),
		onSuccess: async (next) => {
			await done(next);
			toast.success(
				"Public port staged",
				"Deploy the route to apply it. The database keeps its current settings until then.",
			);
		},
		onError: (error) =>
			setRefusal(publicPortError(errorCode(error), errorMessage(error))),
	});

	const close = useMutation({
		...disableDatabasePublicPortMutation(),
		onSuccess: async (next) => {
			await done(next);
			toast.success(
				"Public port closed",
				"The proxy stops publishing it right away.",
			);
		},
	});

	const askClose = () =>
		void confirm({
			title: `Close the public port of ${service.name}?`,
			body: `${service.name} stops accepting connections on port ${view.listenPort ?? ""} of ${view.serverName || "its server"} right away (the route is removed). Clients that use the public address can no longer connect. The database and its data stay as they are.`,
			cta: "Close public port",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await close.mutateAsync({ path: { serviceId } });
			},
		});

	const toggle = (next: boolean) => {
		if (next) {
			setOpening(true);
			setEdited(null);
			return;
		}
		if (view.enabled) {
			askClose();
			return;
		}
		setOpening(false);
		setEdited(null);
		setRefusal(null);
	};

	const submit = () => {
		if (!problems.valid || save.isPending) return;
		save.mutate({
			path: { serviceId },
			body: toSetRequest(form, service.slug),
		});
	};

	const portProblem =
		refusal?.field === "port"
			? refusal.message
			: form.port.trim() !== ""
				? problems.port
				: undefined;
	const otherRefusal =
		refusal !== null && refusal.field !== "port" ? refusal.message : null;

	return (
		<div className="flex flex-col gap-3">
			<div className="flex items-start gap-3">
				<div className="min-w-0 flex-1">
					<label
						htmlFor={`${ids}-switch`}
						className="font-medium text-ink-1 text-sm"
					>
						Expose on a public port
					</label>
					<p className="type-secondary mt-0.5 leading-[1.4]">
						Publish this database on a port of its server so clients outside
						NebulaCtrl can connect. Only the networks you allow get through. The
						proxy forwards raw TCP.
					</p>
				</div>
				{status ? (
					<Badge tone={status.tone} lamp>
						{status.label}
					</Badge>
				) : null}
				<Switch
					id={`${ids}-switch`}
					checked={editing}
					disabled={close.isPending}
					onCheckedChange={toggle}
				/>
			</div>

			{status && (view.staged || view.enabled) ? (
				<div className="flex flex-wrap items-center gap-2">
					<p className="text-ink-2 text-xs">{status.detail}</p>
					{view.staged && routeId ? (
						<Button
							variant="secondary"
							size="xs"
							onClick={() => openStagedReview({ serviceId: routeId })}
						>
							Review and deploy
						</Button>
					) : null}
				</div>
			) : null}

			{editing ? (
				<>
					<div className="flex flex-col gap-1.5">
						<label
							htmlFor={`${ids}-port`}
							className="font-medium text-ink-2 text-xs"
						>
							Port on the server
						</label>
						<Input
							id={`${ids}-port`}
							inputMode="numeric"
							autoComplete="off"
							className="max-w-[160px] font-mono"
							aria-invalid={portProblem !== undefined}
							value={form.port}
							onChange={(event) => set({ port: event.target.value })}
						/>
						<p className="text-2xs text-ink-3">
							1024 to 65535.{" "}
							{view.suggestedPort > 0
								? `${view.suggestedPort} is free on ${view.serverName || service.server?.name || "the server"}.`
								: ""}
						</p>
						{portProblem ? <FieldError>{portProblem}</FieldError> : null}
					</div>

					{recreate ? (
						<Alert variant="warning" role="status">
							<AlertDescription>{RECREATE_PROXY_NOTE}</AlertDescription>
						</Alert>
					) : null}

					<AccessRulesEditor
						allow={form.allow}
						deny={form.deny}
						check={problems.check}
						allowRequired
						onAllowChange={(allow) => set({ allow })}
						onDenyChange={(deny) => set({ deny })}
					/>
					{refusal?.field === "allow" ? (
						<FieldError>{refusal.message}</FieldError>
					) : null}

					{open ? (
						<div className="flex flex-col gap-1.5">
							<Alert variant="warning">
								<AlertDescription>
									This lets every address connect to the database. Type{" "}
									<code className="font-mono text-ink-1">{service.slug}</code>{" "}
									to confirm.
								</AlertDescription>
							</Alert>
							<Input
								aria-label="Type the database slug to confirm"
								autoComplete="off"
								spellCheck={false}
								className="max-w-[320px] font-mono"
								placeholder={service.slug}
								value={form.confirm}
								onChange={(event) => set({ confirm: event.target.value })}
							/>
							{refusal?.field === "confirm" ? (
								<FieldError>{refusal.message}</FieldError>
							) : null}
						</div>
					) : null}

					{warnings.length > 0 ? (
						<Alert variant="warning">
							<AlertDescription>
								{warnings.map((warning) => (
									<p key={warning}>{warning}</p>
								))}
							</AlertDescription>
						</Alert>
					) : (
						<Alert variant="warning">
							<AlertDescription>{PLAINTEXT_NOTE}</AlertDescription>
						</Alert>
					)}

					{otherRefusal ? <FieldError>{otherRefusal}</FieldError> : null}

					<div className="flex flex-wrap items-center gap-2">
						<Button
							variant="primary"
							disabled={!problems.valid || !dirty || save.isPending}
							onClick={submit}
						>
							{save.isPending ? <Spinner /> : null}
							{view.enabled ? "Save changes" : "Expose port"}
						</Button>
						<Button
							variant="secondary"
							disabled={save.isPending || (view.enabled && edited === null)}
							onClick={() => toggle(false)}
						>
							{view.enabled ? "Discard" : "Cancel"}
						</Button>
						{view.enabled ? (
							<Button
								variant="ghost"
								className="ml-auto text-destructive-text"
								disabled={save.isPending || close.isPending}
								onClick={askClose}
							>
								Close public port
							</Button>
						) : null}
						{dirty && problems.reason ? (
							<span className="basis-full text-ink-3 text-xs" role="status">
								{problems.reason}
							</span>
						) : null}
					</div>

					{view.enabled ? (
						<PublicConnection serviceId={serviceId} view={view} />
					) : null}
				</>
			) : null}
		</div>
	);
}

/**
 * A managed database's public port, in its Networking settings: an off switch
 * by default; on, a port, the mandatory allow list (and an optional deny list)
 * and what applying it does to the proxy. Saving only stages a TCP route; the
 * route is deployed like any other staged change.
 */
export function PublicPortSection({ service }: { service: Service }) {
	const view = useQuery({
		...getDatabasePublicPortOptions({ path: { serviceId: service.id } }),
		refetchInterval: (query: {
			state: { data: PublicPortView | undefined };
		}) => {
			const data = query.state.data;
			return data && (data.staged || (data.enabled && !data.deployed))
				? PUBLIC_PORT_POLL_MS
				: false;
		},
	});
	return (
		<div className="flex flex-col gap-3">
			<span className="font-medium text-ink-1 text-sm">Public port</span>
			{view.isPending ? (
				<Skeleton className="h-14 w-full" />
			) : view.isError ? (
				<div className="flex items-center gap-2">
					<FieldError className="flex-1">{errorMessage(view.error)}</FieldError>
					<Button
						variant="secondary"
						size="xs"
						onClick={() => void view.refetch()}
					>
						Retry
					</Button>
				</div>
			) : (
				<PublicPortBody service={service} view={view.data} />
			)}
		</div>
	);
}
