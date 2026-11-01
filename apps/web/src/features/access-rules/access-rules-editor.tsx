import { cn } from "cn";
import { BanIcon, CheckIcon } from "lucide-react";
import { type ReactNode, useId, useMemo } from "react";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { FieldError } from "#/components/ui/field";
import { CidrListEditor } from "./cidr-list";
import { checkRules, describeRules, type RulesCheck } from "./rules";

/**
 * The effective rules in the order they apply: every denied network, then
 * every allowed one, then the default for everything else. A compact
 * `surface-well` panel; each row is a number, an icon and words (the icon is
 * never the only cue).
 */
function RulesPreview({
	allow,
	deny,
	allowRequired = false,
	className,
}: {
	allow: readonly string[];
	deny: readonly string[];
	/** An empty allow list lets nobody in, instead of everyone. */
	allowRequired?: boolean;
	className?: string;
}) {
	const rows = useMemo(
		() => describeRules(allow, deny, { allowRequired }),
		[allow, deny, allowRequired],
	);
	return (
		<section
			aria-label="Effective rules"
			className={cn(
				"surface-well flex flex-col gap-1.5 rounded-ctl p-2.5",
				className,
			)}
		>
			<h4 className="type-micro">Effective rules, first to last</h4>
			<ol className="flex flex-col gap-0.5">
				{rows.map((row) => {
					const Icon = row.effect === "deny" ? BanIcon : CheckIcon;
					return (
						<li
							key={`${row.position}-${row.text}`}
							className="flex items-center gap-2 text-xs"
						>
							<span className="w-4 shrink-0 text-right font-mono text-2xs text-ink-4 tabular-nums">
								{row.position}
							</span>
							<Icon
								aria-hidden
								className={cn(
									"size-3.5 shrink-0",
									row.effect === "deny" ? "text-bad" : "text-ok",
								)}
							/>
							{row.network ? (
								<span className="min-w-0 truncate text-ink-2">
									<span>{row.effect === "deny" ? "Deny" : "Allow"}</span>{" "}
									<span className="font-mono text-ink-1">{row.network}</span>
								</span>
							) : (
								<span className="text-ink-3">{row.text}</span>
							)}
						</li>
					);
				})}
			</ol>
		</section>
	);
}

export interface AccessRulesEditorProps {
	allow: readonly string[];
	deny: readonly string[];
	onAllowChange(next: string[]): void;
	onDenyChange(next: string[]): void;
	/**
	 * What is wrong with the pair. Computed from the lists when omitted; pass
	 * one when the caller adds rules of its own (at least one entry, say).
	 */
	check?: RulesCheck;
	/** Keys of the per-row errors in `check.entries`. */
	allowKey?: string;
	denyKey?: string;
	/**
	 * The allow list may not be empty: an empty one would let nobody in, so the
	 * "empty means everyone" sentence goes away (a public database port).
	 */
	allowRequired?: boolean;
	/** What an empty required allow list says; the default tells the reader nothing can connect yet. */
	allowEmptyHint?: string;
	/** A note on what the protocol can enforce, above the lists. */
	note?: ReactNode;
	/** Lists are shown but cannot be changed. */
	disabled?: boolean;
	/** Hide the preview, for a caller that draws its own summary. */
	hidePreview?: boolean;
	/** Labels of the two lists; the singular names each row ("Allowed network 2"). */
	allowLabel?: string;
	denyLabel?: string;
}

function ListHead({
	id,
	title,
	count,
	children,
}: {
	id: string;
	title: string;
	count: number;
	children: ReactNode;
}) {
	return (
		<div className="flex flex-col gap-0.5">
			<div className="flex items-baseline gap-2">
				<span id={id} className="font-medium text-ink-1 text-sm">
					{title}
				</span>
				{count > 0 ? (
					<span className="font-mono text-2xs text-ink-4 tabular-nums">
						{count}
					</span>
				) : null}
			</div>
			<p className="text-ink-3 text-xs leading-[1.5]">{children}</p>
		</div>
	);
}

/**
 * Who may connect: an allow list and a deny list of networks, and the ordered
 * preview of what they add up to. Deny wins over allow. An empty allow list
 * means everyone except the denied networks, unless `allowRequired`. Used by a
 * domain's access options, TCP routes and a database's public port.
 */
export function AccessRulesEditor({
	allow,
	deny,
	onAllowChange,
	onDenyChange,
	check,
	allowKey = "allowCidrs",
	denyKey = "denyCidrs",
	allowRequired = false,
	allowEmptyHint = "Add at least one allowed network. Nothing can connect until you do.",
	note,
	disabled = false,
	hidePreview = false,
	allowLabel = "Allowed network",
	denyLabel = "Denied network",
}: AccessRulesEditorProps) {
	const id = useId();
	const found = useMemo(
		() =>
			check ?? checkRules(allow, deny, { allowRequired, allowKey, denyKey }),
		[check, allow, deny, allowRequired, allowKey, denyKey],
	);
	const allowCount = allow.filter((entry) => entry.trim() !== "").length;
	const denyCount = deny.filter((entry) => entry.trim() !== "").length;
	// A required allow list that is still empty is guidance, not a red error.
	const allowEmpty = allowRequired && allowCount === 0;
	return (
		<div className="flex flex-col gap-4">
			{note ? (
				<Alert variant="info" role="note">
					<AlertDescription>{note}</AlertDescription>
				</Alert>
			) : null}
			<section className="flex flex-col gap-2" aria-labelledby={`${id}-allow`}>
				<ListHead
					id={`${id}-allow`}
					title="Allowed networks"
					count={allowCount}
				>
					{allowRequired
						? "Only these networks can connect."
						: "Only these networks can connect. Leave it empty to allow everyone except the denied networks."}
				</ListHead>
				<CidrListEditor
					label={allowLabel}
					values={allow}
					errors={found.entries}
					errorPrefix={allowKey}
					disabled={disabled}
					onChange={onAllowChange}
				/>
				{allowEmpty ? (
					<p className="text-ink-3 text-xs">{allowEmptyHint}</p>
				) : found.allow ? (
					<FieldError>{found.allow}</FieldError>
				) : null}
			</section>
			<section className="flex flex-col gap-2" aria-labelledby={`${id}-deny`}>
				<ListHead id={`${id}-deny`} title="Denied networks" count={denyCount}>
					Refused first, even when an allowed network covers them.
				</ListHead>
				<CidrListEditor
					label={denyLabel}
					values={deny}
					errors={found.entries}
					errorPrefix={denyKey}
					addLabel="Add denied network"
					disabled={disabled}
					onChange={onDenyChange}
				/>
				{found.deny ? <FieldError>{found.deny}</FieldError> : null}
			</section>
			{found.rules ? <FieldError>{found.rules}</FieldError> : null}
			{hidePreview ? null : (
				<RulesPreview allow={allow} deny={deny} allowRequired={allowRequired} />
			)}
		</div>
	);
}
