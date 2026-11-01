import { useQueries, useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { LockIcon } from "lucide-react";
import {
	type ChangeEvent,
	type KeyboardEvent,
	type ReactNode,
	useId,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import {
	environmentSharedVariablesQuery,
	servicesQuery,
	suggestedVariablesQuery,
	teamSharedVariablesQuery,
	variablesQuery,
} from "#/api/hooks";
import {
	type NameService,
	type NameSuggestion,
	type NameSuggestionInput,
	suggestNames,
} from "./name-suggestions";

type NameSources = Pick<
	NameSuggestionInput,
	"serviceKind" | "services" | "shared" | "team" | "envExample"
>;

type ExportList = readonly {
	key: string;
	description: string;
	secret: boolean;
}[];

/** Stable, so TanStack can share the combined result between renders. */
const exportsOf = (
	results: readonly { data?: { exports: ExportList } | undefined }[],
) => results.map((result) => result.data?.exports ?? []);

/**
 * What the Name field can suggest: the exports of the environment's other
 * services, the shared variables, the `.env.example` keys of the service's
 * repository, and the kind of the service. Names only: no variable value is
 * read. Nothing is fetched until `enabled`.
 */
export function useNameSources(
	environmentId: string,
	serviceId: string,
	enabled: boolean,
): NameSources {
	const services = useQuery({ ...servicesQuery(environmentId), enabled });
	const shared = useQuery({
		...environmentSharedVariablesQuery(environmentId),
		enabled,
	});
	// A member limited to some projects has no access to team variables: no list.
	const team = useQuery({
		...teamSharedVariablesQuery(),
		enabled,
		retry: false,
	});
	const suggested = useQuery({
		...suggestedVariablesQuery(serviceId),
		enabled,
	});
	const others = useMemo(
		() => (services.data ?? []).filter((service) => service.id !== serviceId),
		[services.data, serviceId],
	);
	const exports = useQueries({
		queries: others.map((service) => ({
			...variablesQuery(service.id),
			enabled,
		})),
		combine: exportsOf,
	});
	const kind = services.data?.find((service) => service.id === serviceId)?.kind;

	return useMemo(() => {
		const named: NameService[] = others.map((service, index) => ({
			slug: service.slug,
			name: service.name,
			exports: (exports[index] ?? []).map((entry) => ({
				key: entry.key,
				...(entry.description ? { description: entry.description } : {}),
				secret: entry.secret,
			})),
		}));
		return {
			serviceKind: kind ?? "app",
			services: named,
			shared: (shared.data ?? []).map((variable) => ({
				key: variable.key,
				layer: variable.layer,
				secret: variable.computedSecret,
			})),
			team: (team.data ?? []).map((variable) => ({
				key: variable.key,
				secret: variable.computedSecret,
			})),
			envExample: (suggested.data?.items ?? [])
				.filter((item) => item.source === "env-example")
				.map((item) => ({
					key: item.key,
					value: item.value,
					needsInput: item.needsInput,
					proposedSecret: item.proposedSecret,
				})),
		};
	}, [others, exports, kind, shared.data, team.data, suggested.data]);
}

const optionId = (listId: string, index: number) => `${listId}-option-${index}`;

export interface UseNameComboboxOptions {
	value: string;
	onValueChange(value: string): void;
	sources: NameSources;
	existingKeys: readonly string[];
	/** False hides the list altogether (a name being edited is read-only). */
	enabled: boolean;
	/** A suggestion was chosen: the caller fills the name, and the value if it has one. */
	onPick(suggestion: NameSuggestion): void;
}

/**
 * Combobox behaviour for the Name field. Spread `fieldProps` on the input and
 * render `popup` in a `relative` wrapper around it. The list opens on focus
 * and while typing; ↑↓ move through the usable entries, Enter or Tab choose,
 * Esc closes the list and not the dialog. With nothing typed no entry is
 * active, so Tab still leaves the field.
 */
export function useNameCombobox({
	value,
	onValueChange,
	sources,
	existingKeys,
	enabled,
	onPick,
}: UseNameComboboxOptions) {
	const listId = useId();
	const fieldRef = useRef<HTMLInputElement>(null);
	const [focused, setFocused] = useState(false);
	const [dismissed, setDismissed] = useState(false);
	const [active, setActive] = useState<number | null>(null);

	const items = useMemo(
		() =>
			enabled ? suggestNames({ ...sources, existingKeys, query: value }) : [],
		[enabled, sources, existingKeys, value],
	);
	const open = enabled && focused && !dismissed && items.length > 0;

	const usable = useMemo(
		() =>
			items.flatMap((item, index) => (item.disabled === true ? [] : [index])),
		[items],
	);
	// Typed text starts on its best match; an empty field waits for an arrow.
	const current =
		active !== null && usable.includes(active)
			? active
			: value.trim() === ""
				? -1
				: (usable[0] ?? -1);

	const pick = (index: number) => {
		const chosen = items[index];
		if (!chosen || chosen.disabled === true) return;
		setDismissed(true);
		onPick(chosen);
	};

	const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		if (!enabled) return;
		if (!open) {
			if (event.key === "ArrowDown" && items.length > 0) {
				event.preventDefault();
				setDismissed(false);
			}
			return;
		}
		switch (event.key) {
			case "ArrowDown":
			case "ArrowUp": {
				if (usable.length === 0) return;
				event.preventDefault();
				const at = usable.indexOf(current);
				const step = event.key === "ArrowDown" ? 1 : -1;
				const next =
					at < 0
						? step === 1
							? 0
							: usable.length - 1
						: (at + step + usable.length) % usable.length;
				setActive(usable[next] ?? null);
				break;
			}
			case "Enter":
			case "Tab":
				// Shift+Tab keeps its job: leave the field.
				if (current < 0 || (event.key === "Tab" && event.shiftKey)) return;
				event.preventDefault();
				pick(current);
				break;
			case "Escape":
				// Close the list, not the dialog around it.
				event.preventDefault();
				event.stopPropagation();
				event.nativeEvent.stopImmediatePropagation();
				setDismissed(true);
				break;
		}
	};

	const fieldProps = {
		ref: fieldRef,
		role: "combobox" as const,
		"aria-autocomplete": "list" as const,
		"aria-expanded": open,
		"aria-controls": open ? listId : undefined,
		"aria-activedescendant":
			open && current >= 0 ? optionId(listId, current) : undefined,
		onChange: (event: ChangeEvent<HTMLInputElement>) => {
			setDismissed(false);
			setActive(null);
			onValueChange(event.target.value);
		},
		onFocus: () => {
			setFocused(true);
			setDismissed(false);
		},
		onBlur: () => setFocused(false),
		onKeyDown,
	};

	const popup: ReactNode = open ? (
		<NameListbox
			id={listId}
			items={items}
			activeIndex={current}
			onPick={pick}
			onHover={setActive}
		/>
	) : null;

	return { fieldRef, fieldProps, popup, open };
}

interface NameListboxProps {
	id: string;
	items: readonly NameSuggestion[];
	activeIndex: number;
	onPick(index: number): void;
	onHover(index: number): void;
}

function NameListbox({
	id,
	items,
	activeIndex,
	onPick,
	onHover,
}: NameListboxProps) {
	const listRef = useRef<HTMLDivElement>(null);

	// Keep the highlighted row in view without scrolling anything else.
	useLayoutEffect(() => {
		const list = listRef.current;
		const row = list?.children[activeIndex] as HTMLElement | undefined;
		if (!list || !row) return;
		if (row.offsetTop < list.scrollTop) list.scrollTop = row.offsetTop;
		else if (
			row.offsetTop + row.offsetHeight >
			list.scrollTop + list.clientHeight
		)
			list.scrollTop = row.offsetTop + row.offsetHeight - list.clientHeight;
	}, [activeIndex]);

	return (
		<div
			data-slot="name-suggestions"
			className="surface-pop absolute inset-x-0 top-full z-20 mt-1 flex flex-col overflow-hidden rounded-menu"
		>
			<div
				ref={listRef}
				id={id}
				role="listbox"
				aria-label="Variable names"
				className="relative max-h-44 overflow-y-auto p-1"
			>
				{items.map((item, index) => (
					// biome-ignore lint/a11y/useFocusableInteractive: options are never focused; the field drives them through aria-activedescendant.
					// biome-ignore lint/a11y/useKeyWithClickEvents: the field handles arrows and Enter for every option; the click is the pointer path to the same pick.
					<div
						key={`${item.source}:${item.from ?? ""}:${item.name}`}
						id={optionId(id, index)}
						role="option"
						aria-selected={index === activeIndex}
						aria-disabled={item.disabled === true || undefined}
						title={item.description}
						// The field keeps focus; the click then picks.
						onMouseDown={(event) => event.preventDefault()}
						onClick={() => onPick(index)}
						onMouseMove={() =>
							item.disabled !== true && index !== activeIndex && onHover(index)
						}
						className={cn(
							"flex min-h-7 items-center gap-2 rounded-md px-2 font-mono text-xs",
							item.disabled === true
								? "cursor-default text-ink-4"
								: "cursor-pointer",
							index === activeIndex ? "bg-white/7 text-ink-1" : "text-ink-2",
						)}
					>
						<span className="min-w-0 flex-1 truncate">
							<NameText item={item} />
							<span className="font-sans text-2xs text-ink-4">
								{originLabel(item)}
							</span>
						</span>
						{item.disabled === true ? (
							<span className="shrink-0 font-sans text-2xs text-ink-4">
								{item.reason}
							</span>
						) : item.secret ? (
							<span
								className="inline-flex shrink-0 items-center gap-1 font-sans text-2xs text-ink-3"
								title="The value is or contains a secret"
							>
								<LockIcon aria-hidden className="size-3" />
								secret
							</span>
						) : null}
					</div>
				))}
			</div>
			<p
				aria-hidden
				className="hidden border-line-faint border-t px-2.5 py-1.5 text-2xs text-ink-4 sm:block"
			>
				↑↓ move · Enter or Tab pick · Esc close
			</p>
			<span role="status" className="sr-only">
				{items.length} suggestion{items.length === 1 ? "" : "s"}
			</span>
		</div>
	);
}

/** The name with the typed text emphasised. */
function NameText({ item }: { item: NameSuggestion }) {
	const { match, name } = item;
	if (!match) return <>{name}</>;
	return (
		<>
			{name.slice(0, match.start)}
			<span className="font-semibold text-ink-1">
				{name.slice(match.start, match.end)}
			</span>
			{name.slice(match.end)}
		</>
	);
}

/** Where a suggestion comes from, e.g. ` · from postgres`; empty for plain names. */
function originLabel(item: NameSuggestion): string {
	switch (item.source) {
		case "export":
		case "env-example":
			return ` · from ${item.from}`;
		case "shared":
			return ` · shared (${item.from})`;
		case "team":
			return " · team";
		case "common":
			return " · common";
		default:
			return "";
	}
}
