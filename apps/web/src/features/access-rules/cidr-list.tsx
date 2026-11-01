import {
	ArrowDownIcon,
	ArrowUpIcon,
	ClipboardPasteIcon,
	PlusIcon,
	XIcon,
} from "lucide-react";
import { useState } from "react";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Textarea } from "#/components/ui/textarea";
import { cleanList, parseEntriesText } from "./rules";

/**
 * An ordered list of networks (CIDRs or single IPs), one input per row with
 * move and remove keys, and a paste box for a whole list (one per line or
 * comma separated). Order is kept as typed. Each row shows its own problem,
 * mirroring what the control plane would answer.
 */
export function CidrListEditor({
	label,
	values,
	errors,
	onChange,
	placeholder = "10.0.0.0/8 or 203.0.113.7",
	errorPrefix = "allowCidrs",
	addLabel = "Add network",
	disabled = false,
}: {
	label: string;
	values: readonly string[];
	errors?: Readonly<Record<string, string>>;
	onChange(next: string[]): void;
	placeholder?: string;
	errorPrefix?: string;
	addLabel?: string;
	disabled?: boolean;
}) {
	const [pasting, setPasting] = useState(false);
	const [pasted, setPasted] = useState("");
	const set = (index: number, value: string) =>
		onChange(values.map((item, i) => (i === index ? value : item)));
	const move = (index: number, step: -1 | 1) => {
		const target = index + step;
		if (target < 0 || target >= values.length) return;
		const next = [...values];
		const [item] = next.splice(index, 1);
		if (item === undefined) return;
		next.splice(target, 0, item);
		onChange(next);
	};
	const addPasted = () => {
		onChange(cleanList([...values, ...parseEntriesText(pasted)]));
		setPasted("");
		setPasting(false);
	};
	return (
		<div className="flex flex-col gap-1.5">
			<ol aria-label={label} className="flex flex-col gap-1.5">
				{values.map((value, index) => {
					const error = errors?.[`${errorPrefix}.${index}`];
					return (
						// biome-ignore lint/suspicious/noArrayIndexKey: rows are positional and have no identity beyond their place in the ordered list
						<li key={index} className="flex flex-col gap-1">
							<div className="flex items-center gap-1">
								<span className="w-5 shrink-0 text-right font-mono text-2xs text-ink-4 tabular-nums">
									{index + 1}
								</span>
								<Input
									aria-label={`${label} ${index + 1}`}
									autoComplete="off"
									spellCheck={false}
									className="font-mono"
									placeholder={placeholder}
									aria-invalid={error !== undefined}
									disabled={disabled}
									value={value}
									onChange={(event) => set(index, event.target.value)}
								/>
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									aria-label={`Move ${label} ${index + 1} up`}
									disabled={disabled || index === 0}
									onClick={() => move(index, -1)}
								>
									<ArrowUpIcon aria-hidden />
								</Button>
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									aria-label={`Move ${label} ${index + 1} down`}
									disabled={disabled || index === values.length - 1}
									onClick={() => move(index, 1)}
								>
									<ArrowDownIcon aria-hidden />
								</Button>
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									aria-label={`Remove ${label} ${index + 1}`}
									disabled={disabled}
									onClick={() => onChange(values.filter((_, i) => i !== index))}
								>
									<XIcon aria-hidden />
								</Button>
							</div>
							{error ? <FieldError className="pl-6">{error}</FieldError> : null}
						</li>
					);
				})}
			</ol>
			{pasting ? (
				<div className="flex flex-col gap-1.5">
					<Textarea
						aria-label={`Paste ${label.toLowerCase()}s`}
						autoComplete="off"
						spellCheck={false}
						className="min-h-20 font-mono text-xs"
						placeholder={"10.0.0.0/8\n192.168.1.20, 2001:db8::/32"}
						value={pasted}
						onChange={(event) => setPasted(event.target.value)}
					/>
					<p className="text-2xs text-ink-3">
						One network per line, or separated by commas.
					</p>
					<div className="flex gap-1.5">
						<Button
							type="button"
							variant="secondary"
							size="xs"
							disabled={parseEntriesText(pasted).length === 0}
							onClick={addPasted}
						>
							Add to list
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="xs"
							onClick={() => {
								setPasted("");
								setPasting(false);
							}}
						>
							Cancel
						</Button>
					</div>
				</div>
			) : (
				<div className="flex gap-1.5">
					<Button
						type="button"
						variant="secondary"
						size="xs"
						disabled={disabled}
						onClick={() => onChange([...values, ""])}
					>
						<PlusIcon aria-hidden />
						{addLabel}
					</Button>
					<Button
						type="button"
						variant="ghost"
						size="xs"
						disabled={disabled}
						onClick={() => setPasting(true)}
					>
						<ClipboardPasteIcon aria-hidden />
						Paste a list
					</Button>
				</div>
			)}
		</div>
	);
}
