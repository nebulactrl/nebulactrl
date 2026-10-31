import { cn } from "cn";
import { type ReactNode, useId, useState } from "react";
import type { ComposeNote } from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { Switch } from "#/components/ui/switch";
import { notesBySeverity } from "./lib";

/** A titled group inside a card: a label and its rows. */
export function Group({
	title,
	children,
	className,
}: {
	title: string;
	children: ReactNode;
	className?: string;
}) {
	return (
		<section
			aria-label={title}
			className={cn(
				"flex flex-col gap-2 border-line-faint border-t pt-3",
				className,
			)}
		>
			<h4 className="type-label m-0">{title}</h4>
			{children}
		</section>
	);
}

/** A label and a line of why on the left, a switch on the right. */
export function ToggleRow({
	label,
	description,
	checked,
	onCheckedChange,
	disabled,
	children,
}: {
	label: ReactNode;
	description?: ReactNode;
	checked: boolean;
	onCheckedChange(next: boolean): void;
	disabled?: boolean;
	/** Extra content under the row, shown as given. */
	children?: ReactNode;
}) {
	const id = useId();
	return (
		<div className="flex flex-col gap-1.5">
			<div className="flex items-start gap-3">
				<div className="flex min-w-0 flex-1 flex-col gap-0.5">
					<label
						htmlFor={id}
						className="cursor-pointer select-none text-[13px] text-ink-1 leading-[1.4]"
					>
						{label}
					</label>
					{description ? (
						<div className="text-[12px] text-ink-3 leading-[1.5]">
							{description}
						</div>
					) : null}
				</div>
				<Switch
					id={id}
					size="sm"
					checked={checked}
					disabled={disabled ?? false}
					onCheckedChange={onCheckedChange}
					className="mt-0.5"
				/>
			</div>
			{children}
		</div>
	);
}

/** A section that opens and closes; its body is not mounted while closed. */
export function Disclosure({
	title,
	count,
	defaultOpen = false,
	children,
}: {
	title: string;
	count?: number;
	defaultOpen?: boolean;
	children: ReactNode;
}) {
	const id = useId();
	const [open, setOpen] = useState(defaultOpen);
	return (
		<div className="flex flex-col border-line-faint border-t pt-3">
			<button
				type="button"
				aria-expanded={open}
				aria-controls={id}
				onClick={() => setOpen(!open)}
				className="type-label flex w-fit cursor-pointer items-center gap-1.5 rounded-sm outline-none hover:text-ink-1 focus-visible:focus-ring"
			>
				<DisclosureChevron open={open} className="size-3.5" />
				{title}
				{count !== undefined ? (
					<span className="font-mono text-2xs text-ink-3 tabular-nums">
						({count})
					</span>
				) : null}
			</button>
			<DisclosurePanel open={open} id={id}>
				<div className="flex flex-col gap-2 pt-2">{children}</div>
			</DisclosurePanel>
		</div>
	);
}

/** Warnings and dangers as alerts (danger stronger); info notes fold away. */
export function NoteAlerts({
	notes,
	ignoredTitle = "Ignored settings",
}: {
	notes: readonly ComposeNote[] | null | undefined;
	ignoredTitle?: string;
}) {
	const by = notesBySeverity(notes);
	return (
		<>
			{by.danger.map((note) => (
				<Alert
					key={`${note.key}:${note.message}`}
					variant="destructive"
					role="note"
				>
					<AlertTitle className="font-mono text-[12px]">{note.key}</AlertTitle>
					<AlertDescription>{note.message}</AlertDescription>
				</Alert>
			))}
			{by.warning.map((note) => (
				<Alert
					key={`${note.key}:${note.message}`}
					variant="warning"
					role="note"
				>
					<AlertTitle className="font-mono text-[12px]">{note.key}</AlertTitle>
					<AlertDescription>{note.message}</AlertDescription>
				</Alert>
			))}
			{by.info.length > 0 ? (
				<Disclosure title={ignoredTitle} count={by.info.length}>
					<ul className="m-0 flex list-none flex-col gap-1.5 p-0">
						{by.info.map((note) => (
							<li
								key={`${note.key}:${note.message}`}
								className="text-[12px] text-ink-3 leading-[1.5]"
							>
								<span className="font-mono text-ink-2">{note.key}</span>
								{": "}
								{note.message}
							</li>
						))}
					</ul>
				</Disclosure>
			) : null}
		</>
	);
}
