import { cn } from "cn";
import { useEffect, useRef } from "react";
import type { SuggestedVariable } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { Checkbox } from "#/components/ui/checkbox";
import { Input } from "#/components/ui/input";
import {
	describeValue,
	type ExampleChoices,
	isChecked,
	orderForReview,
	type ValueDescription,
} from "./env-example";

/**
 * The keys of a repository's `.env.example`, one row each, for review: every
 * key can be unchecked; a key that needs your own value is an input (focused
 * first); the example's comment is the hint; the proposed value says what it
 * will be (a generated secret, a reference to a database, the example's text).
 */
export function EnvExampleList({
	items,
	choices,
	onChoices,
	idPrefix,
	autoFocus = false,
	label = "Keys from .env.example",
}: {
	items: readonly SuggestedVariable[];
	choices: ExampleChoices;
	onChoices(next: ExampleChoices): void;
	/** Keeps ids apart when two lists are on a page. */
	idPrefix: string;
	/** Focus the first key that needs a value when the list mounts. */
	autoFocus?: boolean;
	label?: string;
}) {
	const ordered = orderForReview(items);
	const firstInput = useRef<HTMLInputElement | null>(null);
	useEffect(() => {
		if (autoFocus) firstInput.current?.focus();
	}, [autoFocus]);
	const firstNeeding = ordered.find((item) => item.needsInput)?.key;

	const update = (key: string, patch: Partial<ExampleChoices[string]>) =>
		onChoices({
			...choices,
			[key]: { checked: true, value: "", ...choices[key], ...patch },
		});

	return (
		<ul
			aria-label={label}
			className="divide-y divide-line-faint overflow-hidden rounded-card border border-line bg-surface-well"
		>
			{ordered.map((item) => {
				const checked = isChecked(choices, item.key);
				const id = `${idPrefix}-${item.key}`;
				const value = describeValue(item.value);
				return (
					<li key={item.key} className="flex items-start gap-3 px-3 py-2.5">
						<Checkbox
							id={`${id}-check`}
							aria-label={`Add ${item.key}`}
							className="mt-0.5"
							checked={checked}
							onCheckedChange={(next) => update(item.key, { checked: next })}
						/>
						<div
							className={cn(
								"flex min-w-0 flex-1 flex-col gap-1",
								!checked && "opacity-60",
							)}
						>
							<label
								htmlFor={item.needsInput ? id : `${id}-check`}
								className="flex min-w-0 cursor-pointer flex-wrap items-center gap-x-2 gap-y-1 font-medium font-mono text-ink-1 text-xs"
							>
								<span className="break-all">{item.key}</span>
								{item.proposedSecret ? <Badge>secret</Badge> : null}
								{item.needsInput ? (
									<Badge tone="warn">Needs a value</Badge>
								) : null}
							</label>
							{item.comment ? (
								<p className="text-ink-3 text-xs leading-snug">
									{item.comment}
								</p>
							) : null}
							{item.needsInput ? (
								<Input
									id={id}
									ref={item.key === firstNeeding ? firstInput : undefined}
									aria-label={`Value for ${item.key}`}
									type={item.proposedSecret ? "password" : "text"}
									autoComplete={item.proposedSecret ? "new-password" : "off"}
									spellCheck={false}
									className="font-mono"
									placeholder="Your value"
									disabled={!checked}
									value={choices[item.key]?.value ?? ""}
									onChange={(event) =>
										update(item.key, { value: event.target.value })
									}
								/>
							) : (
								<ProposedValue value={value} />
							)}
							{(item.warnings ?? []).map((warning) => (
								<p key={warning} className="text-status-staged-on text-xs">
									{warning}
								</p>
							))}
						</div>
					</li>
				);
			})}
		</ul>
	);
}

function ProposedValue({ value }: { value: ValueDescription }) {
	switch (value.kind) {
		case "generated":
			return (
				<p className="text-ink-3 text-xs">
					A random value is generated when you add it. It is stored as a secret
					and never shown.
				</p>
			);
		case "reference":
			return (
				<p className="text-ink-3 text-xs">
					Points at <span className="font-mono text-ink-2">{value.target}</span>
					.
				</p>
			);
		case "literal":
			return (
				<p className="truncate font-mono text-ink-2 text-xs" title={value.text}>
					{value.text}
				</p>
			);
	}
}
