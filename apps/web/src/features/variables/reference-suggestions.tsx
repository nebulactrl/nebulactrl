import { useQueries, useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { LockIcon } from "lucide-react";
import {
	type ChangeEvent,
	type KeyboardEvent,
	type ReactNode,
	type SyntheticEvent,
	useEffect,
	useId,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import {
	environmentSharedVariablesQuery,
	servicesQuery,
	teamSharedVariablesQuery,
	variablesQuery,
} from "#/api/hooks";
import type { Variable } from "#/api/models";
import { secretManagersQuery } from "#/features/secret-managers/queries";
import {
	applyCompletion,
	buildCandidates,
	filterCandidates,
	findOpenReference,
	type ReferenceCandidate,
} from "./reference-autocomplete";

/** Stable, so TanStack can share the combined result between renders. */
const dataOf = (results: readonly { data?: VariablesData | undefined }[]) =>
	results.map((result) => result.data);

type VariablesData = {
	items: readonly Variable[];
	exports: readonly { key: string; description: string; secret: boolean }[];
};

/**
 * What a `${{` can point at: the other services of the environment with the
 * names of their variables (never the values), their exports and the
 * platform's; shared variables; this service's own keys; and the functions.
 * Nothing is fetched until `enabled`, so a field that never sees a `${{`
 * costs no request.
 */
export function useReferenceCandidates(
	environmentId: string,
	serviceId: string,
	enabled: boolean,
): ReferenceCandidate[] {
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
	const managers = useQuery({
		...secretManagersQuery(),
		enabled,
		retry: false,
	});
	const own = useQuery({ ...variablesQuery(serviceId), enabled });
	const others = useMemo(
		() => (services.data ?? []).filter((service) => service.id !== serviceId),
		[services.data, serviceId],
	);
	const variables = useQueries({
		queries: others.map((service) => ({
			...variablesQuery(service.id),
			enabled,
		})),
		combine: dataOf,
	});
	return useMemo(
		() =>
			buildCandidates(
				others.map((service, index) => ({
					id: service.id,
					slug: service.slug,
					name: service.name,
					variables: (variables[index]?.items ?? []).map((variable) => ({
						key: variable.key,
						isSecret: variable.computedSecret,
					})),
					exports: (variables[index]?.exports ?? []).map((exported) => ({
						key: exported.key,
						description: exported.description,
						secret: exported.secret,
					})),
				})),
				serviceId,
				{
					own: (own.data?.items ?? []).map((variable) => ({
						key: variable.key,
						isSecret: variable.computedSecret,
					})),
					shared: (shared.data ?? []).map((variable) => ({
						key: variable.key,
						isSecret: variable.computedSecret,
						layer: variable.layer,
					})),
					team: (team.data ?? []).map((variable) => ({
						key: variable.key,
						isSecret: variable.computedSecret,
					})),
					vault: (managers.data ?? []).map((manager) => ({
						name: manager.name,
						kind: manager.kind,
					})),
					functions: true,
				},
			),
		[
			others,
			variables,
			serviceId,
			own.data,
			shared.data,
			team.data,
			managers.data,
		],
	);
}

const optionId = (listId: string, index: number) => `${listId}-option-${index}`;

type Field = HTMLInputElement | HTMLTextAreaElement;

export interface UseReferenceInputOptions {
	value: string;
	onValueChange(value: string): void;
	candidates: readonly ReferenceCandidate[];
	/**
	 * Where the list opens: under a single-line field, or docked inside the
	 * bottom edge of a tall one (a textarea).
	 */
	placement?: "below" | "inside-bottom";
}

/**
 * Reference autocomplete for a text field. Spread `fieldProps` on the input or
 * textarea and render `popup` in a `relative` wrapper around it. While the
 * caret sits inside an unclosed `${{`, the list shows matching `slug.KEY`
 * references: arrows move, Enter or Tab insert, Esc closes.
 */
export function useReferenceInput<E extends Field>({
	value,
	onValueChange,
	candidates,
	placement = "below",
}: UseReferenceInputOptions) {
	const listId = useId();
	const fieldRef = useRef<E>(null);
	const pendingCaret = useRef<number | null>(null);
	const [caret, setCaret] = useState(0);
	const [focused, setFocused] = useState(false);
	const [dismissedAt, setDismissedAt] = useState<number | null>(null);
	const [active, setActive] = useState(0);

	const token = useMemo(
		() => (focused ? findOpenReference(value, caret) : null),
		[focused, value, caret],
	);
	const query = token?.query;
	const matches = useMemo(
		() => (query === undefined ? [] : filterCandidates(candidates, query)),
		[candidates, query],
	);
	const open =
		token !== null && matches.length > 0 && dismissedAt !== token.start;
	const activeIndex = Math.min(active, Math.max(matches.length - 1, 0));

	// A new partial starts the list from its best match again.
	// biome-ignore lint/correctness/useExhaustiveDependencies: reset on every new partial
	useEffect(() => setActive(0), [query]);
	// Esc only hides the list for the `${{` it was pressed in.
	useEffect(() => {
		if (token === null) setDismissedAt(null);
	}, [token]);

	// After an insertion React rewrites the field's value, which parks the
	// caret at the end; put it right after the inserted reference.
	// biome-ignore lint/correctness/useExhaustiveDependencies: runs once per value change
	useLayoutEffect(() => {
		const next = pendingCaret.current;
		if (next === null) return;
		pendingCaret.current = null;
		fieldRef.current?.setSelectionRange(next, next);
		setCaret(next);
	}, [value]);

	const pick = (candidate: ReferenceCandidate) => {
		if (token === null) return;
		const result = applyCompletion(value, token, candidate);
		pendingCaret.current = result.caret;
		onValueChange(result.text);
	};

	const syncCaret = (event: SyntheticEvent<E>) =>
		setCaret(
			event.currentTarget.selectionStart ?? event.currentTarget.value.length,
		);

	const onKeyDown = (event: KeyboardEvent<E>) => {
		if (!open || token === null) return;
		const count = matches.length;
		switch (event.key) {
			case "ArrowDown":
				event.preventDefault();
				setActive((activeIndex + 1) % count);
				break;
			case "ArrowUp":
				event.preventDefault();
				setActive((activeIndex - 1 + count) % count);
				break;
			case "Enter":
			case "Tab": {
				const chosen = matches[activeIndex];
				// Shift+Tab keeps its job: leave the field.
				if (!chosen || (event.key === "Tab" && event.shiftKey)) return;
				event.preventDefault();
				pick(chosen);
				break;
			}
			case "Escape":
				// Close the list, not the dialog around it.
				event.preventDefault();
				event.stopPropagation();
				event.nativeEvent.stopImmediatePropagation();
				setDismissedAt(token.start);
				break;
		}
	};

	const fieldProps = {
		ref: fieldRef,
		role: "combobox" as const,
		"aria-autocomplete": "list" as const,
		"aria-expanded": open,
		"aria-controls": open ? listId : undefined,
		"aria-activedescendant": open ? optionId(listId, activeIndex) : undefined,
		onChange: (event: ChangeEvent<E>) => {
			onValueChange(event.target.value);
			syncCaret(event);
		},
		onSelect: syncCaret,
		onFocus: (event: SyntheticEvent<E>) => {
			setFocused(true);
			syncCaret(event);
		},
		onBlur: () => setFocused(false),
		onKeyDown,
	};

	const popup: ReactNode = open ? (
		<ReferenceListbox
			id={listId}
			matches={matches}
			activeIndex={activeIndex}
			onPick={pick}
			onHover={setActive}
			placement={placement}
		/>
	) : null;

	return { fieldProps, popup, open };
}

interface ReferenceListboxProps {
	id: string;
	matches: readonly ReferenceCandidate[];
	activeIndex: number;
	onPick(candidate: ReferenceCandidate): void;
	onHover(index: number): void;
	placement: "below" | "inside-bottom";
}

function ReferenceListbox({
	id,
	matches,
	activeIndex,
	onPick,
	onHover,
	placement,
}: ReferenceListboxProps) {
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
			data-slot="reference-suggestions"
			className={cn(
				"surface-pop absolute z-20 flex flex-col overflow-hidden rounded-menu",
				placement === "below"
					? "inset-x-0 top-full mt-1"
					: "inset-x-2 bottom-2",
			)}
		>
			<div
				ref={listRef}
				id={id}
				role="listbox"
				aria-label="Variable references"
				className="relative max-h-36 overflow-y-auto p-1"
			>
				{matches.map((candidate, index) => (
					// biome-ignore lint/a11y/useFocusableInteractive: options are never focused; the field drives them through aria-activedescendant.
					// biome-ignore lint/a11y/useKeyWithClickEvents: the field handles arrows and Enter for every option; the click is the pointer path to the same pick.
					<div
						key={candidate.insert}
						id={optionId(id, index)}
						role="option"
						aria-selected={index === activeIndex}
						// The field keeps focus; the click then inserts.
						onMouseDown={(event) => event.preventDefault()}
						onClick={() => onPick(candidate)}
						onMouseMove={() => index !== activeIndex && onHover(index)}
						className={cn(
							"flex min-h-7 cursor-pointer items-center gap-2 rounded-md px-2 font-mono text-xs",
							index === activeIndex ? "bg-white/7 text-ink-1" : "text-ink-2",
						)}
					>
						<span className="min-w-0 flex-1 truncate" title={candidate.insert}>
							{candidate.kind === "vault" ? (
								<>
									<span className="text-ink-3">{candidate.slug}:</span>
									{candidate.key}
									{candidate.slug === "vault" ? (
										<span className="text-ink-3">/</span>
									) : null}
								</>
							) : (
								<>
									{candidate.slug ? (
										<span className="text-ink-3">{candidate.slug}.</span>
									) : null}
									{candidate.key}
								</>
							)}
							{candidate.description ? (
								<span className="ml-2 font-sans text-2xs text-ink-4">
									{candidate.description}
								</span>
							) : null}
						</span>
						{candidate.isSecret ? (
							<span
								className="inline-flex shrink-0 items-center gap-1 font-sans text-2xs text-ink-3"
								title="The value is or contains a secret"
							>
								<LockIcon aria-hidden className="size-3" />
								secret
							</span>
						) : (
							<CandidateBadge candidate={candidate} />
						)}
					</div>
				))}
			</div>
			<p
				aria-hidden
				className="hidden border-line-faint border-t px-2.5 py-1.5 text-2xs text-ink-4 sm:block"
			>
				↑↓ move · Enter or Tab insert · Esc close
			</p>
		</div>
	);
}

const BADGE_LABEL: Record<ReferenceCandidate["kind"], string | null> = {
	variable: null,
	own: "this service",
	platform: "platform",
	export: "export",
	shared: "shared",
	team: "team",
	vault: "secret manager",
	function: "function",
};

function CandidateBadge({ candidate }: { candidate: ReferenceCandidate }) {
	const label =
		candidate.kind === "shared" && candidate.layer
			? `shared · ${candidate.layer}`
			: BADGE_LABEL[candidate.kind];
	if (!label) return null;
	return (
		<span className="shrink-0 font-sans text-2xs text-ink-4">{label}</span>
	);
}
