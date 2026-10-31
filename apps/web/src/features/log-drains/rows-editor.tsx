import { PlusIcon, XIcon } from "lucide-react";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import { newRow, type Row } from "./model";

export interface RowsEditorProps {
	rows: readonly Row[];
	onChange(next: Row[]): void;
	/** Names the group for assistive tech: "Labels", "Headers". */
	label: string;
	namePlaceholder?: string;
	valuePlaceholder?: string;
	/** "Add label". */
	addLabel: string;
	/** Hide what is typed in the value column (header values are secret). */
	secret?: boolean;
	/** Mono names, for header and field names. */
	mono?: boolean;
}

/**
 * An editable list of name and value pairs: labels, resource attributes, extra
 * fields and request headers. A pair is two fields and a remove button; the
 * list grows with "Add". A row left blank is ignored when the form is sent.
 */
export function RowsEditor({
	rows,
	onChange,
	label,
	namePlaceholder = "name",
	valuePlaceholder = "value",
	addLabel,
	secret = false,
	mono = true,
}: RowsEditorProps) {
	const update = (id: number, patch: Partial<Row>) =>
		onChange(rows.map((row) => (row.id === id ? { ...row, ...patch } : row)));
	return (
		<fieldset className="m-0 flex min-w-0 flex-col gap-2 border-0 p-0">
			<legend className="sr-only">{label}</legend>
			{rows.map((row) => (
				<div
					key={row.id}
					className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] items-center gap-2"
				>
					<Input
						aria-label={`${label}: name`}
						autoComplete="off"
						spellCheck={false}
						className={mono ? "font-mono" : undefined}
						placeholder={namePlaceholder}
						value={row.name}
						onChange={(e) => update(row.id, { name: e.target.value })}
					/>
					<Input
						aria-label={`${label}: value${row.name ? ` of ${row.name}` : ""}`}
						type={secret ? "password" : "text"}
						autoComplete={secret ? "new-password" : "off"}
						spellCheck={false}
						className={mono ? "font-mono" : undefined}
						placeholder={valuePlaceholder}
						value={row.value}
						onChange={(e) => update(row.id, { value: e.target.value })}
					/>
					<Button
						type="button"
						variant="ghost"
						size="icon-sm"
						aria-label={`Remove ${row.name || "row"}`}
						onClick={() => onChange(rows.filter((r) => r.id !== row.id))}
					>
						<XIcon aria-hidden />
					</Button>
				</div>
			))}
			<div>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					onClick={() => onChange([...rows, newRow()])}
				>
					<PlusIcon aria-hidden />
					{addLabel}
				</Button>
			</div>
		</fieldset>
	);
}
