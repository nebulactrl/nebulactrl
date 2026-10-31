import { useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import {
	EllipsisVerticalIcon,
	FileTextIcon,
	HistoryIcon,
	LockIcon,
	PencilIcon,
	PlusIcon,
	RefreshCwIcon,
	Trash2Icon,
	Undo2Icon,
} from "lucide-react";
import { useId, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { pendingChangesQuery, variablesQuery } from "#/api/hooks";
import type {
	ExportVariable,
	PlatformVariable,
	Service,
	Variable,
} from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { Input } from "#/components/ui/input";
import { SectionLabel } from "#/components/ui/section-label";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import {
	CREDENTIAL_LOCK_MESSAGE,
	lockedCredentialKeys,
} from "#/features/databases/credentials";
import { databaseQuery, isManagedDatabase } from "#/features/databases/queries";
import { FieldMark } from "#/features/project-config";
import { ConnectionVariables } from "./connection-variables";
import { EnvExampleBanner } from "./env-example-banner";
import { HistoryDialog } from "./history-dialog";
import { RawEnvDialog } from "./raw-env-dialog";
import { RotateDialog } from "./rotate-dialog";
import {
	SuggestedVariables,
	type SuggestionDraft,
} from "./suggested-variables";
import { useVariableMutations } from "./use-variable-mutations";
import { VariableDialog } from "./variable-dialog";

/** What a secret shows in place of its value; the API never returns one. */
const SECRET_MASK = "••••";

const CATEGORY_NOTE: Record<PlatformVariable["category"], string> = {
	release: "with the release",
	"release-self": "once the source is pinned",
	deployment: "per deployment",
	container: "per container",
};

/** A filter box appears once a list outgrows a glance. */
const FILTER_FROM = 8;

const VARIABLE_FIELD = "variables.";

const ROW_GRID =
	"grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)_auto] items-center gap-x-2.5 @xl:grid-cols-[minmax(0,14rem)_minmax(0,1fr)_auto]";

/**
 * Variables: a header with the two ways in (one at a time, or the raw `.env`
 * editor), the list, and a disclosure row for what the platform provides on
 * its own. Secrets show `••••` and cannot be revealed: the API never returns
 * them. A variable with a staged change carries a `staged` marker and can be
 * reverted to what the running release has.
 */
export function VariablesTab({ service }: { service: Service }) {
	const variables = useQuery(variablesQuery(service.id));
	const pending = useQuery({
		...pendingChangesQuery(service.id),
		enabled: service.pendingChangeCounts.variables > 0,
	});
	const mutations = useVariableMutations(service.id);
	const database = useQuery({
		...databaseQuery(service.id),
		enabled: isManagedDatabase(service),
	});
	const lockedKeys = lockedCredentialKeys(database.data);
	const confirm = useConfirm();
	const [editing, setEditing] = useState<Variable | "new" | null>(null);
	const [draft, setDraft] = useState<SuggestionDraft | null>(null);
	const [historyKey, setHistoryKey] = useState<string | null>(null);
	const [rotateKey, setRotateKey] = useState<string | null>(null);
	const [rawOpen, setRawOpen] = useState(false);
	const [filter, setFilter] = useState("");

	// Staged variable changes by key; a removal has an empty `to`. An inherited
	// change has unchanged text: a variable it references changed (`via`).
	const staged = useMemo(() => {
		const changed = new Set<string>();
		const inherited = new Map<string, string>();
		const removed: string[] = [];
		for (const change of pending.data ?? []) {
			if (!change.field.startsWith(VARIABLE_FIELD)) continue;
			const key = change.field.slice(VARIABLE_FIELD.length);
			changed.add(key);
			if (change.origin === "inherited") inherited.set(key, change.via ?? "");
			else if (change.to === "") removed.push(key);
		}
		return { changed, inherited, removed };
	}, [pending.data]);

	if (variables.isPending) {
		return (
			<div
				className="flex flex-col gap-2 p-4"
				role="status"
				aria-label="Loading variables"
			>
				<Skeleton className="h-8 w-full" />
				<Skeleton className="h-8 w-full" />
				<Skeleton className="h-8 w-full" />
			</div>
		);
	}
	if (variables.isError) {
		return (
			<ErrorState
				className="m-4"
				title="Variables did not load"
				body={errorMessage(variables.error)}
				retry={() => void variables.refetch()}
			/>
		);
	}

	const { items, platform, exports } = variables.data;
	const keys = new Set(items.map((variable) => variable.key));
	// Removed since the release, so no longer in the list: shown so the removal
	// is visible and can be taken back.
	const removed = staged.removed.filter((key) => !keys.has(key)).sort();
	// A managed database lists what it offers on top, apart from the user's own
	// variables; every other service keeps its exports in the platform disclosure.
	const connection = database.data?.exports ?? [];

	const showFilter = items.length > FILTER_FROM;
	const needle = showFilter ? filter.trim().toLowerCase() : "";
	const visible = needle
		? items.filter(
				(variable) =>
					variable.key.toLowerCase().includes(needle) ||
					(!variable.isSecret &&
						(variable.value ?? "").toLowerCase().includes(needle)),
			)
		: items;

	const remove = async (variable: Variable) => {
		const ok = await confirm({
			title: `Delete ${variable.key}?`,
			body: "The variable is removed from the service's next deployment. The running release keeps its current environment until you deploy.",
			cta: "Delete variable",
			glyph: "trash",
			danger: true,
			hold: false,
			onConfirm: () => mutations.remove.mutateAsync(variable.key),
		});
		if (ok)
			toast.success(`Deleted ${variable.key}`, "Staged until the next deploy.");
	};

	const revert = (key: string) =>
		mutations.discard.mutate(key, {
			onSuccess: () => toast.success(`Reverted ${key}`),
			onError: (error) =>
				toast.error(`Could not revert ${key}`, errorMessage(error)),
		});

	return (
		<section
			data-tab="variables"
			data-service-id={service.id}
			aria-label="Variables"
			className="@container flex min-h-full flex-col gap-4 p-4"
		>
			<ConnectionVariables serviceId={service.id} exports={connection} />

			<div className="flex flex-col gap-2">
				<SectionLabel
					aside={
						<>
							<Button
								variant="secondary"
								size="md"
								onClick={() => setRawOpen(true)}
							>
								<FileTextIcon aria-hidden />
								Raw editor
							</Button>
							<Button size="md" onClick={() => setEditing("new")}>
								<PlusIcon aria-hidden />
								New variable
							</Button>
						</>
					}
				>
					Service variables
				</SectionLabel>
				<p className="type-secondary">
					Changes are staged and reach the container on the next deploy.
				</p>
			</div>

			<EnvExampleBanner service={service} />

			{showFilter ? (
				<Input
					type="search"
					aria-label="Filter variables"
					placeholder="Filter by name or value"
					autoComplete="off"
					spellCheck={false}
					value={filter}
					onChange={(event) => setFilter(event.target.value)}
				/>
			) : null}

			{items.length === 0 && removed.length === 0 ? (
				<EmptyState
					illustration="key"
					title="No variables yet"
					body="Add one with New variable, or paste a whole .env file in the Raw editor."
				/>
			) : visible.length === 0 && (needle !== "" || removed.length === 0) ? (
				<p className="type-secondary px-1" role="status">
					No variable matches “{filter.trim()}”.
				</p>
			) : (
				<ul
					aria-label="Service variables"
					className="surface-raised divide-y divide-line-faint rounded-card"
				>
					{visible.map((variable) => (
						<VariableRow
							key={variable.key}
							serviceId={service.id}
							variable={variable}
							locked={lockedKeys.has(variable.key)}
							staged={staged.changed.has(variable.key)}
							inheritedVia={staged.inherited.get(variable.key)}
							busy={mutations.discard.isPending}
							onEdit={() => setEditing(variable)}
							onHistory={() => setHistoryKey(variable.key)}
							onRotate={() => setRotateKey(variable.key)}
							onDelete={() => void remove(variable)}
							onRevert={() => revert(variable.key)}
						/>
					))}
					{needle
						? null
						: removed.map((key) => (
								<RemovedRow
									key={key}
									name={key}
									busy={mutations.discard.isPending}
									onRevert={() => revert(key)}
								/>
							))}
				</ul>
			)}
			{needle && visible.length > 0 && visible.length < items.length ? (
				<p className="type-secondary px-1" aria-live="polite">
					Showing {visible.length} of {items.length}.
				</p>
			) : null}

			<SuggestedVariables
				serviceId={service.id}
				onNeedsInput={(next) => {
					setDraft(next);
					setEditing("new");
				}}
			/>

			<PlatformVariables
				variables={platform}
				exports={connection.length > 0 ? [] : exports}
			/>

			<VariableDialog
				serviceId={service.id}
				environmentId={service.environmentId}
				target={editing}
				draft={draft}
				canBuild={service.sourceType === "git"}
				existingKeys={items.map((variable) => variable.key)}
				onClose={() => {
					setEditing(null);
					setDraft(null);
				}}
			/>
			<HistoryDialog
				serviceId={service.id}
				variableKey={historyKey}
				onClose={() => setHistoryKey(null)}
			/>
			<RotateDialog
				serviceId={service.id}
				variableKey={rotateKey}
				onClose={() => setRotateKey(null)}
			/>
			<RawEnvDialog
				serviceId={service.id}
				environmentId={service.environmentId}
				open={rawOpen}
				variables={items}
				onOpenChange={setRawOpen}
			/>
		</section>
	);
}

function VariableRow({
	serviceId,
	variable,
	staged,
	inheritedVia,
	locked,
	busy,
	onEdit,
	onHistory,
	onRotate,
	onDelete,
	onRevert,
}: {
	serviceId: string;
	variable: Variable;
	staged: boolean;
	/** Set when the staged change comes from a referenced variable, not this one. */
	inheritedVia: string | undefined;
	/** A credential of a deployed managed database: rotate it, do not edit it. */
	locked: boolean;
	busy: boolean;
	onEdit(): void;
	onHistory(): void;
	onRotate(): void;
	onDelete(): void;
	onRevert(): void;
}) {
	const generated = variable.generated
		.map((call) => `${call.fn}(${(call.args ?? []).join(", ")})`)
		.join(" + ");
	const shared = variable.references.some((ref) => ref.ns === "shared");
	const buildScoped = variable.scope !== "runtime";
	return (
		<li
			className={cn(ROW_GRID, "px-3 py-2 font-medium font-mono text-[11.5px]")}
		>
			<span className="flex min-w-0 items-center gap-1.5">
				<span title={variable.key} className="truncate text-ink-2">
					{variable.key}
				</span>
				<FieldMark field={`variables.${variable.key}`} serviceId={serviceId} />
				{staged ? (
					<Badge tone="staged" className="shrink-0">
						staged
					</Badge>
				) : null}
			</span>
			<span className="flex min-w-0 flex-col gap-1 text-ink-3">
				<span className="flex min-w-0 items-center gap-2">
					{variable.isSecret ? (
						<>
							<span aria-hidden className="tracking-[0.04em]">
								{SECRET_MASK}
							</span>
							<span className="sr-only">Secret value, hidden</span>
							<Badge title="Secret: the value is never shown">
								<LockIcon aria-hidden />
								secret
							</Badge>
						</>
					) : variable.value ? (
						<span
							title={variable.value}
							className="truncate tracking-[0.02em] @max-xl:whitespace-normal @max-xl:break-all"
						>
							{variable.value}
						</span>
					) : (
						<span className="text-ink-4">empty</span>
					)}
					{locked ? (
						<Badge title={CREDENTIAL_LOCK_MESSAGE}>
							<LockIcon aria-hidden />
							managed
						</Badge>
					) : null}
				</span>
				{variable.isSecret ||
				variable.computedSecret ||
				generated ||
				shared ||
				buildScoped ||
				variable.references.length > 0 ||
				variable.rotationDue ||
				variable.problem ||
				inheritedVia !== undefined ? (
					<span className="flex flex-wrap items-center gap-1 font-sans font-normal">
						{generated ? (
							<Badge title="Generated once on the server; never regenerated by a deploy">
								generated · {generated}
							</Badge>
						) : null}
						{!variable.isSecret && variable.computedSecret ? (
							<Badge title="Contains a secret through a reference: shown masked in logs">
								<LockIcon aria-hidden />
								contains a secret
							</Badge>
						) : null}
						{shared ? <Badge>shared</Badge> : null}
						{buildScoped ? (
							<Badge title="Passed to the image build">{variable.scope}</Badge>
						) : null}
						{variable.references.length > 0 ? (
							<Badge
								title={variable.references
									.map((ref) => (ref.ns ? `${ref.ns}.${ref.key}` : ref.key))
									.join(", ")}
							>
								references {variable.references.length}
							</Badge>
						) : null}
						{variable.rotationDue ? (
							<Badge tone="warn" title="Its rotation reminder is due">
								rotation due
							</Badge>
						) : variable.isSecret && variable.ageDays >= 30 ? (
							<Badge title="Time since the value last changed">
								age {variable.ageDays}d
							</Badge>
						) : null}
						{inheritedVia !== undefined ? (
							<span className="text-[11px] text-warn">
								restaged: {inheritedVia || "a referenced variable"} changed
							</span>
						) : null}
						{variable.problem ? (
							<span className="text-[11px] text-warn" title={variable.problem}>
								does not resolve: {variable.problem}
							</span>
						) : null}
					</span>
				) : null}
			</span>
			<span className="flex items-center gap-1.5">
				<DropdownMenu>
					<DropdownMenuTrigger
						render={
							<Button
								variant="ghost"
								size="icon-sm"
								aria-label={`Actions for ${variable.key}`}
							/>
						}
					>
						<EllipsisVerticalIcon aria-hidden />
					</DropdownMenuTrigger>
					<DropdownMenuContent align="end" className="w-[190px]">
						<DropdownMenuItem
							render={<button type="button" />}
							disabled={locked}
							onClick={onEdit}
						>
							<PencilIcon aria-hidden />
							Edit
						</DropdownMenuItem>
						<DropdownMenuItem
							render={<button type="button" />}
							onClick={onHistory}
						>
							<HistoryIcon aria-hidden />
							History
						</DropdownMenuItem>
						{variable.source === "generated" && !locked ? (
							<DropdownMenuItem
								render={<button type="button" />}
								onClick={onRotate}
							>
								<RefreshCwIcon aria-hidden />
								Rotate…
							</DropdownMenuItem>
						) : null}
						{staged && inheritedVia === undefined ? (
							<DropdownMenuItem
								render={<button type="button" />}
								disabled={busy}
								onClick={onRevert}
							>
								<Undo2Icon aria-hidden />
								Revert staged change
							</DropdownMenuItem>
						) : null}
						<DropdownMenuSeparator />
						<DropdownMenuItem
							render={<button type="button" />}
							variant="destructive"
							disabled={locked}
							onClick={onDelete}
						>
							<Trash2Icon aria-hidden />
							Delete
						</DropdownMenuItem>
						{locked ? (
							<p className="px-2 py-1.5 text-2xs text-ink-3">
								{CREDENTIAL_LOCK_MESSAGE}
							</p>
						) : null}
					</DropdownMenuContent>
				</DropdownMenu>
			</span>
		</li>
	);
}

/** A variable whose removal is staged: gone from the list, still in the release. */
function RemovedRow({
	name,
	busy,
	onRevert,
}: {
	name: string;
	busy: boolean;
	onRevert(): void;
}) {
	return (
		<li
			className={cn(ROW_GRID, "px-3 py-2 font-medium font-mono text-[11.5px]")}
		>
			<span title={name} className="truncate text-ink-4 line-through">
				{name}
			</span>
			<span className="truncate text-ink-3">Removed on next deploy</span>
			<span className="flex items-center gap-1.5">
				<Badge tone="staged">staged</Badge>
				<Button
					variant="ghost"
					size="icon-sm"
					aria-label={`Restore ${name}`}
					title={`Restore ${name}`}
					disabled={busy}
					onClick={onRevert}
				>
					<Undo2Icon aria-hidden />
				</Button>
			</span>
		</li>
	);
}

/** What the platform injects; read-only, and a user variable cannot shadow it. */
function PlatformVariables({
	variables,
	exports,
}: {
	variables: readonly PlatformVariable[];
	exports: readonly ExportVariable[];
}) {
	const [open, setOpen] = useState(false);
	const listId = useId();
	if (variables.length === 0 && exports.length === 0) return null;
	return (
		<div className="surface-raised rounded-card">
			<button
				type="button"
				aria-expanded={open}
				aria-controls={listId}
				onClick={() => setOpen((value) => !value)}
				className="flex w-full cursor-pointer items-center gap-2 rounded-card px-3 py-2.5 text-left text-[13px] text-ink-1 outline-none focus-visible:focus-ring"
			>
				<DisclosureChevron open={open} className="size-3.5 text-ink-3" />
				<span className="min-w-0 font-medium">
					{variables.length} variable{variables.length === 1 ? "" : "s"}{" "}
					provided by NebulaCtrl
					{exports.length > 0
						? `, ${exports.length} export${exports.length === 1 ? "" : "s"}`
						: ""}
				</span>
				<span className="ml-auto shrink-0 text-ink-3 text-xs">
					{open ? "Hide" : "Show"}
				</span>
			</button>
			<DisclosurePanel open={open} id={listId}>
				<ul
					aria-label="Variables provided by NebulaCtrl"
					className="divide-y divide-line-faint border-line-faint border-t"
				>
					{variables.map((variable) => (
						<li
							key={variable.key}
							className={cn(ROW_GRID, "items-start px-3 py-2")}
						>
							<span
								title={variable.key}
								className="truncate font-medium font-mono text-[11.5px] text-ink-2"
							>
								{variable.key}
							</span>
							<span className="col-span-2 min-w-0">
								<span
									title={variable.value}
									className="block truncate font-medium font-mono text-[11.5px] text-ink-3"
								>
									{variable.value || "empty"}
								</span>
								<span className="mt-0.5 block text-[11px] text-ink-4 leading-snug">
									{variable.description}
									{variable.referenceable
										? ""
										: ` Set ${CATEGORY_NOTE[variable.category]}; other services cannot reference it.`}
								</span>
							</span>
						</li>
					))}
					{exports.map((exported) => (
						<li
							key={`export-${exported.key}`}
							className={cn(ROW_GRID, "items-start px-3 py-2")}
						>
							<span
								title={exported.key}
								className="truncate font-medium font-mono text-[11.5px] text-ink-2"
							>
								{exported.key}
							</span>
							<span className="col-span-2 min-w-0">
								<span className="flex items-center gap-2 font-medium font-mono text-[11.5px] text-ink-3">
									{exported.secret ? SECRET_MASK : "computed"}
									<Badge title="Computed for references like ${{slug.KEY}}; not stored and not injected into this service">
										export
									</Badge>
								</span>
								<span className="mt-0.5 block text-[11px] text-ink-4 leading-snug">
									{exported.description}
								</span>
							</span>
						</li>
					))}
				</ul>
			</DisclosurePanel>
		</div>
	);
}
