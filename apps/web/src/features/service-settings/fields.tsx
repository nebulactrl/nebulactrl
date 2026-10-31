import { cn } from "cn";
import { createContext, type ReactNode, useContext } from "react";
import { InfoTip } from "#/components/settings/info-tip";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { FieldMark } from "#/features/project-config";
import type { SettingsDraft } from "./draft";
import { sectionHead } from "./settings-chrome";
import type { Category } from "./settings-nav-model";
import type { TailnetErrorKey } from "./tailnet-draft";

/**
 * The category whose page the sections are on: a section whose head repeats
 * the category's heading drops the repeat. Absent outside the service
 * Settings tab (project settings, dialogs), where a section keeps its head.
 */
const CategoryHeadContext = createContext<Pick<
	Category,
	"label" | "description"
> | null>(null);

export function CategoryHeadProvider({
	category,
	children,
}: {
	category: Pick<Category, "label" | "description">;
	children: ReactNode;
}) {
	return (
		<CategoryHeadContext.Provider value={category}>
			{children}
		</CategoryHeadContext.Provider>
	);
}

export type DraftErrors = Partial<
	Record<keyof SettingsDraft | TailnetErrorKey, string>
>;

/** What every section receives: the working copy, its problems, and a setter. */
export interface DraftProps {
	draft: SettingsDraft;
	errors: DraftErrors;
	set<K extends keyof SettingsDraft>(key: K, value: SettingsDraft[K]): void;
}

/**
 * A section under a `■ TITLE ····` header, a line of why, and ruled rows (no
 * card). It is its own `@container/section`, so rows go two-column when it
 * is wide (the settings panel is shown at ~66vw) and stack when it is not.
 */
export function Section({
	title,
	description,
	aside,
	tone = "default",
	hidden = false,
	children,
	field,
}: {
	title: string;
	description?: ReactNode;
	aside?: ReactNode;
	/** `danger` frames the card in the destructive colour. */
	tone?: "default" | "danger";
	/** Stays mounted (so its state survives) but is not shown. */
	hidden?: boolean;
	children: ReactNode;
	/** The nebula.toml field group this section sets (`scale`): marks it when the file owns any of it. */
	field?: string;
}) {
	const head = sectionHead(useContext(CategoryHeadContext), {
		title,
		description,
	});
	// A title that repeats the category's still shows when the head carries a mark or an aside.
	const showHeading = head.showTitle || Boolean(field) || Boolean(aside);
	const showDescription = Boolean(description) && head.showDescription;
	return (
		<section
			aria-label={title}
			data-anchor={title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}
			className={cn(
				"@container/section flex flex-col gap-2",
				hidden && "hidden",
			)}
		>
			{showHeading || showDescription ? (
				<header className="flex flex-col gap-1.5">
					{showHeading ? (
						<h3
							className={cn(
								"section-head type-micro",
								tone === "danger" && "text-status-failed-on",
							)}
						>
							<span>{title}</span>
							{field ? (
								<span className="font-sans normal-case tracking-normal">
									<FieldMark field={field} />
								</span>
							) : null}
							{aside ? (
								<span className="font-sans text-ink-3 text-xs normal-case tracking-normal">
									{aside}
								</span>
							) : null}
						</h3>
					) : null}
					{showDescription ? (
						<p className="type-secondary">{description}</p>
					) : null}
				</header>
			) : null}
			<div
				className={cn(
					"rule-rows",
					tone === "danger" && "[&>*]:border-destructive-border",
				)}
			>
				{children}
			</div>
		</section>
	);
}

/**
 * Free-form content inside a `Section` that is not a label/control row.
 * `anchor` lets the settings search jump to it.
 */
export function SectionBody({
	anchor,
	className,
	children,
}: {
	anchor?: string;
	className?: string;
	children: ReactNode;
}) {
	return (
		<div data-anchor={anchor} className={className}>
			{children}
		</div>
	);
}

/**
 * One setting: label and a line of why on the left (220px), the control on
 * the right. Decided by the width of the section card, not the viewport.
 */
export function SettingRow({
	label,
	description,
	info,
	htmlFor,
	anchor,
	children,
	className,
	field,
}: {
	label: ReactNode;
	description?: ReactNode;
	/** The longer explanation behind a one-line description, as an info tip. Never a warning. */
	info?: ReactNode;
	/** Id of the control this row labels, so the label becomes its <label>. */
	htmlFor?: string;
	/** Lets the settings search jump to a row that has no control id. */
	anchor?: string;
	children: ReactNode;
	className?: string;
	/** The field id nebula.toml may own (`config.port`): marks the row when it does. */
	field?: string;
}) {
	const labelClassName = "font-medium text-ink-1 text-sm leading-[1.25]";
	return (
		<div
			data-slot="setting-row"
			data-anchor={anchor}
			className={cn(
				"grid grid-cols-1 items-start gap-2 py-1.5 @md/section:grid-cols-[220px_minmax(0,1fr)] @md/section:gap-6",
				className,
			)}
		>
			<div className="flex min-w-0 flex-col gap-1 @md/section:pt-1.5">
				<div className="flex min-w-0 items-center gap-1.5">
					{htmlFor ? (
						<label htmlFor={htmlFor} className={labelClassName}>
							{label}
						</label>
					) : (
						<span className={labelClassName}>{label}</span>
					)}
					{field ? <FieldMark field={field} /> : null}
					{info ? (
						<InfoTip
							label={
								typeof label === "string"
									? `About ${label.toLowerCase()}`
									: "More information"
							}
						>
							{info}
						</InfoTip>
					) : null}
				</div>
				{description ? (
					<span className="type-secondary leading-[1.4]">{description}</span>
				) : null}
			</div>
			<div className="flex min-w-0 flex-col gap-1.5">{children}</div>
		</div>
	);
}

export interface TextRowProps {
	id: string;
	label: ReactNode;
	description?: ReactNode;
	info?: ReactNode;
	value: string;
	onChange(value: string): void;
	error?: string | undefined;
	mono?: boolean;
	placeholder?: string;
	type?: "text" | "password" | "number";
	disabled?: boolean;
	autoComplete?: string;
	narrow?: boolean;
	/** The nebula.toml field id this row sets, to mark it when the file owns it. */
	field?: string;
}

export function TextRow({
	id,
	label,
	description,
	info,
	value,
	onChange,
	error,
	mono = false,
	placeholder,
	type = "text",
	disabled = false,
	autoComplete = "off",
	narrow = false,
	field,
}: TextRowProps) {
	return (
		<SettingRow
			label={label}
			htmlFor={id}
			{...(description ? { description } : {})}
			{...(info ? { info } : {})}
			{...(field ? { field } : {})}
		>
			<Input
				id={id}
				type={type === "number" ? "text" : type}
				inputMode={type === "number" ? "decimal" : undefined}
				autoComplete={autoComplete}
				spellCheck={false}
				disabled={disabled}
				placeholder={placeholder}
				value={value}
				aria-invalid={error !== undefined}
				aria-describedby={error ? `${id}-error` : undefined}
				className={cn(
					(mono || type === "number") && "font-mono",
					narrow && "max-w-[160px]",
				)}
				onChange={(event) => onChange(event.target.value)}
			/>
			<FieldError id={`${id}-error`}>{error}</FieldError>
		</SettingRow>
	);
}

export interface SelectOption {
	value: string;
	label: ReactNode;
	/** Plain text for the closed trigger. */
	text: string;
	/** Shown but not choosable (a server that is being drained). */
	disabled?: boolean;
}

export function SelectRow({
	id,
	label,
	description,
	info,
	value,
	options,
	onChange,
	error,
	placeholder,
	field,
}: {
	id: string;
	label: ReactNode;
	description?: ReactNode;
	info?: ReactNode;
	value: string;
	options: readonly SelectOption[];
	onChange(value: string): void;
	error?: string | undefined;
	placeholder?: string;
	/** The nebula.toml field id this row sets, to mark it when the file owns it. */
	field?: string;
}) {
	const items = options.map((option) => ({
		value: option.value,
		label: option.text,
	}));
	return (
		<SettingRow
			label={label}
			htmlFor={id}
			{...(description ? { description } : {})}
			{...(info ? { info } : {})}
			{...(field ? { field } : {})}
		>
			<Select
				value={value}
				items={items}
				onValueChange={(next) => onChange(String(next ?? ""))}
			>
				<SelectTrigger
					id={id}
					className="w-full max-w-[360px]"
					aria-invalid={error !== undefined}
				>
					<SelectValue placeholder={placeholder} />
				</SelectTrigger>
				<SelectContent alignItemWithTrigger={false}>
					{options.map((option) => (
						<SelectItem
							key={option.value}
							value={option.value}
							disabled={option.disabled ?? false}
						>
							{option.label}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<FieldError>{error}</FieldError>
		</SettingRow>
	);
}
