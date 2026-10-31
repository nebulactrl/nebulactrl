import { type ReactNode, useId, useState } from "react";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { FieldError } from "#/components/ui/field";
import { Spinner } from "#/components/ui/spinner";

/**
 * One collapsible group of a domain's options. The header says what is set
 * and whether the group has a staged change (the same Staged badge the card
 * and the tab strip use); the footer appears once the group is edited and
 * saves or discards that group alone.
 */
export function OptionGroupPanel({
	title,
	summary,
	staged,
	stagedTitle,
	dirty,
	invalid,
	saving,
	error,
	notice,
	locked = false,
	defaultOpen = false,
	onSave,
	onDiscard,
	children,
}: {
	title: string;
	/** What is set, as a few words; empty when nothing is. */
	summary: string;
	staged: boolean;
	stagedTitle?: string;
	dirty: boolean;
	invalid: boolean;
	saving: boolean;
	/** The control plane's answer to the last save, when it was not a field error. */
	error?: string | undefined;
	/** A line above the fields, e.g. "A redirect has no options." */
	notice?: ReactNode;
	/** The group cannot be edited right now (a redirect domain, an old agent). */
	locked?: boolean;
	defaultOpen?: boolean;
	onSave(): void;
	onDiscard(): void;
	children: ReactNode;
}) {
	const [open, setOpen] = useState(defaultOpen);
	const bodyId = useId();
	return (
		<section
			aria-label={title}
			className="surface-well flex flex-col rounded-card"
		>
			<button
				type="button"
				aria-expanded={open}
				aria-controls={bodyId}
				onClick={() => setOpen(!open)}
				className="flex min-h-10 w-full cursor-pointer items-center gap-2 rounded-card px-3 py-2 text-left outline-none focus-visible:focus-ring"
			>
				<DisclosureChevron open={open} className="size-3.5 text-ink-3" />
				<span className="font-medium text-[13px] text-ink-1">{title}</span>
				{summary ? (
					<span className="min-w-0 truncate text-ink-3 text-xs">{summary}</span>
				) : null}
				<span className="ml-auto flex shrink-0 items-center gap-1.5">
					{dirty ? (
						<Badge tone="warn" shape="tag">
							Unsaved
						</Badge>
					) : null}
					{staged ? (
						<Badge
							tone="staged"
							{...(stagedTitle ? { title: stagedTitle } : {})}
						>
							Staged
						</Badge>
					) : null}
				</span>
			</button>
			<DisclosurePanel open={open} id={bodyId}>
				<div className="flex flex-col gap-3 border-line border-t px-3 py-3">
					{notice ? (
						<p className="text-ink-3 text-xs leading-[1.5]">{notice}</p>
					) : null}
					<fieldset
						disabled={locked}
						className="m-0 flex min-w-0 flex-col gap-4 border-0 p-0 disabled:opacity-55"
					>
						{children}
					</fieldset>
					{error ? <FieldError>{error}</FieldError> : null}
					{dirty ? (
						<div className="flex items-center justify-end gap-2">
							<Button
								type="button"
								variant="ghost"
								size="sm"
								disabled={saving}
								onClick={onDiscard}
							>
								Discard
							</Button>
							<Button
								type="button"
								variant="secondary"
								size="sm"
								disabled={invalid || saving || locked}
								onClick={onSave}
							>
								{saving ? <Spinner /> : null}
								Save {title.toLowerCase()}
							</Button>
						</div>
					) : null}
				</div>
			</DisclosurePanel>
		</section>
	);
}
