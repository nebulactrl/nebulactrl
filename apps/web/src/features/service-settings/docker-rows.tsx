import { PlusIcon, XIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import type { DockerField, DockerForm, DockerIssue } from "./docker-options";

/** What every Docker option row receives. */
export interface DockerRowProps {
	form: DockerForm;
	issues: readonly DockerIssue[];
	update(patch: Partial<DockerForm>): void;
}

/** The problems of one field: the list-wide one and one per row. */
export interface RowIssues {
	listWide: string | undefined;
	row(index: number): string | undefined;
}

/** What one row editor needs: the rows' problems by row, and the list-wide one. */
export function issuesFor(
	issues: readonly DockerIssue[],
	field: DockerField,
): RowIssues {
	const own = issues.filter((issue) => issue.field === field);
	return {
		listWide: own.find((issue) => issue.row === undefined)?.message,
		row: (index: number) => own.find((issue) => issue.row === index)?.message,
	};
}

/** The shared look of one editable row: inputs, then a remove key. */
export function RowShell({
	label,
	index,
	error,
	onRemove,
	disabled = false,
	children,
}: {
	label: string;
	index: number;
	error: string | undefined;
	onRemove(): void;
	disabled?: boolean;
	children: ReactNode;
}) {
	return (
		<li className="flex flex-col gap-1">
			<div className="flex items-center gap-1.5">
				{children}
				<Button
					type="button"
					variant="ghost"
					size="icon-sm"
					disabled={disabled}
					aria-label={`Remove ${label} ${index + 1}`}
					onClick={onRemove}
				>
					<XIcon aria-hidden />
				</Button>
			</div>
			<FieldError>{error}</FieldError>
		</li>
	);
}

export function AddRowButton({
	children,
	disabled = false,
	onClick,
}: {
	children: ReactNode;
	disabled?: boolean;
	onClick(): void;
}) {
	return (
		<div>
			<Button
				type="button"
				variant="secondary"
				size="xs"
				disabled={disabled}
				onClick={onClick}
			>
				<PlusIcon aria-hidden />
				{children}
			</Button>
		</div>
	);
}

/** Replaces row `index` of a list with `patch` applied. */
export function patchRow<T>(
	rows: readonly T[],
	index: number,
	patch: Partial<T>,
): T[] {
	return rows.map((row, i) => (i === index ? { ...row, ...patch } : row));
}

/**
 * A list of single text inputs with a remove key each and an add key under
 * them: DNS servers, supplementary groups.
 */
export function StringList({
	label,
	rowLabel,
	values,
	issues,
	max,
	placeholder,
	addLabel,
	inputMode,
	onChange,
}: {
	/** Names the list for assistive tech. */
	label: string;
	/** Singular, for "Remove DNS server 2". */
	rowLabel: string;
	values: readonly string[];
	issues: RowIssues;
	max: number;
	placeholder: string;
	addLabel: string;
	inputMode?: "numeric";
	onChange(values: string[]): void;
}) {
	return (
		<>
			{values.length > 0 ? (
				<ul aria-label={label} className="flex flex-col gap-1.5">
					{values.map((value, index) => (
						<RowShell
							// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional inputs without identity
							key={index}
							label={rowLabel}
							index={index}
							error={issues.row(index)}
							onRemove={() => onChange(values.filter((_, i) => i !== index))}
						>
							<Input
								aria-label={`${rowLabel[0]?.toUpperCase()}${rowLabel.slice(1)} ${index + 1}`}
								autoComplete="off"
								spellCheck={false}
								inputMode={inputMode}
								className="max-w-[260px] font-mono"
								placeholder={placeholder}
								aria-invalid={issues.row(index) !== undefined}
								value={value}
								onChange={(event) =>
									onChange(
										values.map((v, i) =>
											i === index ? event.target.value : v,
										),
									)
								}
							/>
						</RowShell>
					))}
				</ul>
			) : null}
			<FieldError>{issues.listWide}</FieldError>
			<AddRowButton
				disabled={values.length >= max}
				onClick={() => onChange([...values, ""])}
			>
				{addLabel}
			</AddRowButton>
		</>
	);
}
