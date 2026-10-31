import { PlusIcon, XIcon } from "lucide-react";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import type { FormErrors, HeaderRow } from "#/features/proxy/lib/options";

/** Name/value rows for custom request or response headers. */
export function HeaderRowsEditor({
	label,
	rows,
	errors,
	errorKey,
	onChange,
}: {
	label: string;
	rows: readonly HeaderRow[];
	errors: FormErrors;
	errorKey: "requestHeaders" | "responseHeaders";
	onChange(next: HeaderRow[]): void;
}) {
	const set = (index: number, patch: Partial<HeaderRow>) =>
		onChange(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)));
	return (
		<div className="flex flex-col gap-1.5">
			<ul aria-label={label} className="flex flex-col gap-1.5">
				{rows.map((row, index) => {
					const nameError = errors[`${errorKey}.${index}.name`];
					const valueError = errors[`${errorKey}.${index}.value`];
					return (
						// biome-ignore lint/suspicious/noArrayIndexKey: header rows are edited in place and have no id
						<li key={index} className="flex flex-col gap-1">
							<div className="flex items-center gap-1.5">
								<Input
									aria-label={`${label} name ${index + 1}`}
									autoComplete="off"
									spellCheck={false}
									className="max-w-[200px] font-mono"
									placeholder="X-Header"
									aria-invalid={nameError !== undefined}
									value={row.name}
									onChange={(event) => set(index, { name: event.target.value })}
								/>
								<Input
									aria-label={`${label} value ${index + 1}`}
									autoComplete="off"
									spellCheck={false}
									className="font-mono"
									placeholder="value"
									aria-invalid={valueError !== undefined}
									value={row.value}
									onChange={(event) =>
										set(index, { value: event.target.value })
									}
								/>
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									aria-label={`Remove ${label} ${index + 1}`}
									onClick={() => onChange(rows.filter((_, i) => i !== index))}
								>
									<XIcon aria-hidden />
								</Button>
							</div>
							{nameError || valueError ? (
								<FieldError>{nameError ?? valueError}</FieldError>
							) : null}
						</li>
					);
				})}
			</ul>
			{errors[errorKey] ? <FieldError>{errors[errorKey]}</FieldError> : null}
			<div>
				<Button
					type="button"
					variant="secondary"
					size="xs"
					onClick={() => onChange([...rows, { name: "", value: "" }])}
				>
					<PlusIcon aria-hidden />
					Add header
				</Button>
			</div>
		</div>
	);
}
