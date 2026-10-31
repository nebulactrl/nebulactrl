import { ChevronDownIcon, ChevronUpIcon, PlusIcon, XIcon } from "lucide-react";
import {
	type ComponentProps,
	type KeyboardEvent,
	useEffect,
	useId,
	useMemo,
	useRef,
	useState,
} from "react";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { count } from "#/lib/format";
import {
	addServices,
	type ComponentRow,
	canMoveGroup,
	canMoveRow,
	editRow,
	groupNames,
	groupRows,
	MAX_COMPONENTS,
	MAX_DISPLAY_NAME,
	MAX_GROUP_NAME,
	moveGroup,
	moveRow,
	type RowProblems,
	removeRow,
	renameGroup,
	rowProblems,
} from "./model";
import { ServicePickerDialog } from "./service-picker-dialog";

const LABEL = "type-label";

/**
 * A text field that keeps what is typed to itself and hands it over when the
 * person leaves the field or presses Enter. Used where a change moves the row
 * (a group): applying it on every key would re-parent the row under the
 * reader's fingers.
 */
function CommitInput({
	value,
	onCommit,
	onKeyDown,
	...props
}: Omit<ComponentProps<typeof Input>, "value" | "onChange"> & {
	value: string;
	onCommit: (value: string) => void;
}) {
	const [draft, setDraft] = useState(value);
	useEffect(() => setDraft(value), [value]);
	const commit = () => {
		if (draft !== value) onCommit(draft);
	};
	return (
		<Input
			{...props}
			value={draft}
			onChange={(event) => setDraft(event.target.value)}
			onBlur={commit}
			onKeyDown={(event) => {
				onKeyDown?.(event);
				if (event.defaultPrevented) return;
				if (event.key === "Enter") {
					event.preventDefault();
					commit();
				}
				if (event.key === "Escape") setDraft(value);
			}}
		/>
	);
}

/** Alt + ↑ / ↓: which way a key asks a row or a group to move, if it does. */
function moveDirection(event: KeyboardEvent): -1 | 1 | null {
	if (!event.altKey) return null;
	if (event.key === "ArrowUp") return -1;
	if (event.key === "ArrowDown") return 1;
	return null;
}

function MoveButtons({
	label,
	canUp,
	canDown,
	onMove,
	register,
}: {
	/** What moves: "api" or "group Core". */
	label: string;
	canUp: boolean;
	canDown: boolean;
	onMove: (direction: -1 | 1) => void;
	register?: (direction: -1 | 1, element: HTMLButtonElement | null) => void;
}) {
	return (
		<span className="flex shrink-0 items-center gap-0.5">
			<Button
				type="button"
				variant="ghost"
				size="icon-sm"
				aria-label={`Move ${label} up`}
				disabled={!canUp}
				ref={(element) => register?.(-1, element)}
				onClick={() => onMove(-1)}
			>
				<ChevronUpIcon aria-hidden />
			</Button>
			<Button
				type="button"
				variant="ghost"
				size="icon-sm"
				aria-label={`Move ${label} down`}
				disabled={!canDown}
				ref={(element) => register?.(1, element)}
				onClick={() => onMove(1)}
			>
				<ChevronDownIcon aria-hidden />
			</Button>
		</span>
	);
}

function ComponentRowEditor({
	row,
	rows,
	problems,
	datalistId,
	onEdit,
	onMove,
	onRemove,
	register,
}: {
	row: ComponentRow;
	rows: readonly ComponentRow[];
	problems: RowProblems | undefined;
	datalistId: string;
	onEdit: (
		edit: Partial<Pick<ComponentRow, "displayName" | "groupName" | "url">>,
	) => void;
	onMove: (direction: -1 | 1) => void;
	onRemove: () => void;
	register: (direction: -1 | 1, element: HTMLButtonElement | null) => void;
}) {
	const id = useId();
	const where =
		row.projectName || row.environmentName
			? `${row.serviceName} · ${row.projectName} · ${row.environmentName}`
			: row.serviceName;
	// Alt + ↑ / ↓ moves the row from the keyboard without leaving the field.
	const moveKeys = (event: KeyboardEvent) => {
		const direction = moveDirection(event);
		if (direction === null) return;
		event.preventDefault();
		onMove(direction);
	};
	return (
		<li className="flex flex-col gap-2 py-3">
			<div className="flex items-center gap-2">
				<MoveButtons
					label={row.displayName.trim() || row.serviceName}
					canUp={canMoveRow(rows, row.serviceId, -1)}
					canDown={canMoveRow(rows, row.serviceId, 1)}
					onMove={onMove}
					register={register}
				/>
				<span
					className="min-w-0 flex-1 truncate font-mono text-2xs text-ink-3"
					title={where}
				>
					{where}
				</span>
				<Button
					type="button"
					variant="ghost"
					size="icon-sm"
					aria-label={`Remove ${row.displayName.trim() || row.serviceName} from the page`}
					onClick={onRemove}
				>
					<XIcon aria-hidden />
				</Button>
			</div>
			<div className="grid grid-cols-1 gap-3 pl-0 sm:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_minmax(0,1.4fr)] sm:pl-[58px]">
				<div className="flex min-w-0 flex-col gap-1">
					<label htmlFor={`${id}-name`} className={LABEL}>
						Public name
					</label>
					<Input
						id={`${id}-name`}
						autoComplete="off"
						maxLength={MAX_DISPLAY_NAME + 20}
						placeholder={row.serviceName}
						aria-invalid={problems?.displayName !== undefined}
						aria-describedby={
							problems?.displayName ? `${id}-name-error` : undefined
						}
						value={row.displayName}
						onChange={(event) => onEdit({ displayName: event.target.value })}
						onKeyDown={moveKeys}
					/>
					{problems?.displayName ? (
						<FieldError id={`${id}-name-error`}>
							{problems.displayName}
						</FieldError>
					) : null}
				</div>
				<div className="flex min-w-0 flex-col gap-1">
					<label htmlFor={`${id}-group`} className={LABEL}>
						Group
					</label>
					<CommitInput
						id={`${id}-group`}
						autoComplete="off"
						list={datalistId}
						placeholder="No group"
						aria-invalid={problems?.groupName !== undefined}
						aria-describedby={
							problems?.groupName ? `${id}-group-error` : undefined
						}
						value={row.groupName}
						onCommit={(groupName) => onEdit({ groupName })}
						onKeyDown={moveKeys}
					/>
					{problems?.groupName ? (
						<FieldError id={`${id}-group-error`}>
							{problems.groupName}
						</FieldError>
					) : null}
				</div>
				<div className="flex min-w-0 flex-col gap-1">
					<label htmlFor={`${id}-url`} className={LABEL}>
						Link (optional)
					</label>
					<Input
						id={`${id}-url`}
						type="url"
						inputMode="url"
						autoComplete="off"
						spellCheck={false}
						placeholder="https://example.com/docs"
						className="font-mono"
						aria-invalid={problems?.url !== undefined}
						aria-describedby={problems?.url ? `${id}-url-error` : undefined}
						value={row.url}
						onChange={(event) => onEdit({ url: event.target.value })}
						onKeyDown={moveKeys}
					/>
					{problems?.url ? (
						<FieldError id={`${id}-url-error`}>{problems.url}</FieldError>
					) : null}
				</div>
			</div>
		</li>
	);
}

/**
 * The page's components: the services visitors see, each under a public name,
 * in groups, with an optional https link. The list is the working copy; the
 * editor's Save sends it whole. Rows and groups move with the up and down
 * buttons, or Alt + ↑ / ↓ from any field of a row.
 */
export function ComponentsEditor({
	pageId,
	rows,
	onChange,
}: {
	pageId: string;
	rows: ComponentRow[];
	onChange: (rows: ComponentRow[]) => void;
}) {
	const datalistId = useId();
	const [picking, setPicking] = useState(false);
	const [note, setNote] = useState("");
	const problems = useMemo(() => rowProblems(rows), [rows]);
	const groups = useMemo(() => groupRows(rows), [rows]);
	const names = useMemo(() => groupNames(rows), [rows]);
	const taken = useMemo(
		() => new Set(rows.map((row) => row.serviceId)),
		[rows],
	);
	const buttons = useRef(new Map<string, HTMLButtonElement>());
	const refocus = useRef<string | null>(null);

	// A moved row keeps the reader's place: focus goes back to the button they
	// pressed, or to its twin when that end of the group has been reached.
	// biome-ignore lint/correctness/useExhaustiveDependencies: runs after every list change
	useEffect(() => {
		const key = refocus.current;
		if (!key) return;
		refocus.current = null;
		const cut = key.lastIndexOf("|");
		const id = key.slice(0, cut);
		const direction = key.slice(cut + 1);
		const first = buttons.current.get(`${id}|${direction}`);
		const other = buttons.current.get(
			`${id}|${direction === "-1" ? "1" : "-1"}`,
		);
		(first && !first.disabled ? first : other)?.focus();
	}, [rows]);

	const register =
		(id: string) => (direction: -1 | 1, element: HTMLButtonElement | null) => {
			const key = `${id}|${direction}`;
			if (element) buttons.current.set(key, element);
			else buttons.current.delete(key);
		};

	const moveOne = (row: ComponentRow, direction: -1 | 1) => {
		const next = moveRow(rows, row.serviceId, direction);
		onChange(next);
		refocus.current = `${row.serviceId}|${direction}`;
		const at = next.findIndex((r) => r.serviceId === row.serviceId) + 1;
		setNote(
			`${row.displayName.trim() || row.serviceName} moved ${direction < 0 ? "up" : "down"}, position ${at} of ${next.length}.`,
		);
	};

	const moveWholeGroup = (name: string, direction: -1 | 1) => {
		const next = moveGroup(rows, name, direction);
		onChange(next);
		refocus.current = `g:${name}|${direction}`;
		const at = groupNames(next).indexOf(name) + 1;
		setNote(
			`Group ${name} moved ${direction < 0 ? "up" : "down"}, group ${at} of ${groupNames(next).length}.`,
		);
	};

	const room = MAX_COMPONENTS - rows.length;
	const showHeadings = names.length > 0;

	return (
		<div className="flex flex-col gap-3">
			<div className="flex flex-wrap items-center justify-between gap-2">
				<p className="m-0 text-ink-3 text-xs">
					{rows.length === 0
						? "Nothing is shown yet."
						: `${count(rows.length, "component")} of ${MAX_COMPONENTS}. Use the arrows, or Alt and ↑ ↓ in a field, to reorder.`}
				</p>
				<Button
					type="button"
					variant="secondary"
					size="sm"
					disabled={room <= 0}
					onClick={() => setPicking(true)}
				>
					<PlusIcon />
					Add services
				</Button>
			</div>

			{rows.length === 0 ? (
				<p className="surface-well m-0 rounded-card p-3 text-ink-3 text-xs leading-relaxed">
					Add the services your visitors care about: the website, the API, the
					dashboard. Each one shows its live state and 90 days of history under
					the public name you choose. A page needs at least one before it can be
					published.
				</p>
			) : (
				<div className="flex flex-col gap-4">
					<datalist id={datalistId}>
						{names.map((name) => (
							<option key={name} value={name} />
						))}
					</datalist>
					{groups.map((group) => (
						<section
							key={`group:${group.name}`}
							aria-label={group.name ? `Group ${group.name}` : "Not in a group"}
							className="flex flex-col"
						>
							{showHeadings ? (
								<header className="flex items-center gap-2 border-line-faint border-b pb-2">
									{group.name ? (
										<>
											<MoveButtons
												label={`group ${group.name}`}
												canUp={canMoveGroup(rows, group.name, -1)}
												canDown={canMoveGroup(rows, group.name, 1)}
												onMove={(direction) =>
													moveWholeGroup(group.name, direction)
												}
												register={register(`g:${group.name}`)}
											/>
											<CommitInput
												aria-label={`Name of group ${group.name}`}
												autoComplete="off"
												className="h-7 max-w-[260px] font-medium"
												maxLength={MAX_GROUP_NAME + 20}
												value={group.name}
												onKeyDown={(event) => {
													const direction = moveDirection(event);
													if (direction === null) return;
													event.preventDefault();
													moveWholeGroup(group.name, direction);
												}}
												onCommit={(next) => {
													if (next.trim() === "") return;
													onChange(renameGroup(rows, group.name, next));
												}}
											/>
											<span className="text-ink-3 text-xs">
												{count(group.rows.length, "component")}
											</span>
										</>
									) : (
										<span className="type-label">
											Not in a group, shown first
										</span>
									)}
								</header>
							) : null}
							<ul
								aria-label={group.name || "Components"}
								className="m-0 flex list-none flex-col divide-y divide-line-faint p-0"
							>
								{group.rows.map((row) => (
									<ComponentRowEditor
										key={row.serviceId}
										row={row}
										rows={rows}
										problems={problems[row.serviceId]}
										datalistId={datalistId}
										register={register(row.serviceId)}
										onEdit={(edit) =>
											onChange(editRow(rows, row.serviceId, edit))
										}
										onMove={(direction) => moveOne(row, direction)}
										onRemove={() => {
											onChange(removeRow(rows, row.serviceId));
											setNote(
												`${row.displayName.trim() || row.serviceName} removed from the page.`,
											);
										}}
									/>
								))}
							</ul>
						</section>
					))}
				</div>
			)}

			<p role="status" aria-live="polite" className="sr-only">
				{note}
			</p>

			<ServicePickerDialog
				key={pageId}
				open={picking}
				onOpenChange={setPicking}
				taken={taken}
				room={room}
				onAdd={(services) => {
					onChange(addServices(rows, services));
					setNote(`${count(services.length, "service")} added to the page.`);
				}}
			/>
		</div>
	);
}
