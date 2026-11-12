import { cn } from "cn";
import { CheckIcon, PlusIcon, XIcon } from "lucide-react";
import {
	type ClipboardEvent,
	type KeyboardEvent,
	useId,
	useState,
} from "react";
import { Button } from "#/components/ui/button";
import { FieldDescription, FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Label } from "#/components/ui/label";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Toggle } from "#/components/ui/toggle";
import {
	COUNTRY_CODES,
	countryName,
	FIELDS,
	type FirewallCondition,
	type FirewallRule,
	fieldInfo,
	MAX_CONDITIONS,
	METHODS,
	opChoices,
	opKey,
	parseCountry,
	type RuleErrors,
	withField,
	withOp,
} from "./model";

/** One datalist of countries per page, shared by every country input. */
export const COUNTRY_LIST_ID = "firewall-countries";

export function CountryOptions() {
	return (
		<datalist id={COUNTRY_LIST_ID}>
			{COUNTRY_CODES.map((code) => (
				<option key={code} value={`${code} · ${countryName(code)}`} />
			))}
		</datalist>
	);
}

/**
 * The values of a condition as chips: type and press Enter (or a comma) to
 * add, paste a list to add them all, Backspace in the empty input removes the
 * last. Methods are a row of keys instead: there are nine of them.
 */
function ValuesInput({
	condition,
	onChange,
	invalid,
	describedBy,
	label,
}: {
	condition: FirewallCondition;
	onChange(values: string[]): void;
	invalid: boolean;
	describedBy?: string | undefined;
	label: string;
}) {
	const [text, setText] = useState("");
	const values = condition.values ?? [];
	const info = fieldInfo(condition.field);
	const country = condition.field === "country";

	if (condition.field === "method") {
		return (
			<fieldset
				aria-label={label}
				className="m-0 flex min-w-0 flex-wrap items-center gap-1 border-0 p-0"
			>
				{METHODS.map((method) => {
					const pressed = values.includes(method);
					return (
						<Toggle
							key={method}
							variant="outline"
							size="sm"
							className="font-mono"
							pressed={pressed}
							onPressedChange={(on) =>
								onChange(
									on
										? METHODS.filter((m) => m === method || values.includes(m))
										: values.filter((v) => v !== method),
								)
							}
						>
							{pressed ? <CheckIcon aria-hidden /> : null}
							{method}
						</Toggle>
					);
				})}
			</fieldset>
		);
	}

	const commit = (raw: string) => {
		const parts = raw
			.split(/[\n,]/)
			.map((part) => part.trim())
			.filter(Boolean)
			.map((part) =>
				country ? (parseCountry(part) ?? part.toUpperCase()) : part,
			);
		if (parts.length === 0) return false;
		const next = [...values];
		for (const part of parts) if (!next.includes(part)) next.push(part);
		onChange(next);
		setText("");
		return true;
	};
	const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		if (event.key === "Enter" || event.key === ",") {
			if (text.trim() !== "") {
				event.preventDefault();
				commit(text);
			} else if (event.key === ",") {
				event.preventDefault();
			}
		} else if (event.key === "Backspace" && text === "" && values.length > 0) {
			onChange(values.slice(0, -1));
		}
	};
	const onPaste = (event: ClipboardEvent<HTMLInputElement>) => {
		const pasted = event.clipboardData.getData("text");
		if (/[\n,]/.test(pasted)) {
			event.preventDefault();
			commit(text + pasted);
		}
	};

	return (
		<div
			className={cn(
				"field flex min-h-8 min-w-0 flex-1 flex-wrap items-center gap-1 px-1.5 py-1",
				invalid && "border-bad",
			)}
		>
			{values.map((value) => (
				<span
					key={value}
					className="inline-flex max-w-full items-center gap-1 rounded-[5px] bg-surface-raised py-0.5 pr-0.5 pl-1.5 text-ink-1 text-xs"
				>
					<span
						className={cn("truncate", (info.mono || country) && "font-mono")}
					>
						{value}
					</span>
					{country ? (
						<span className="truncate text-ink-3">{countryName(value)}</span>
					) : null}
					<button
						type="button"
						className="pressable inline-flex size-4 shrink-0 items-center justify-center rounded-[4px] text-ink-3 hover:text-ink-1 focus-visible:focus-ring"
						aria-label={`Remove ${value}`}
						onClick={() => onChange(values.filter((v) => v !== value))}
					>
						<XIcon aria-hidden className="size-3" />
					</button>
				</span>
			))}
			<input
				aria-label={label}
				aria-invalid={invalid || undefined}
				aria-describedby={describedBy}
				list={country ? COUNTRY_LIST_ID : undefined}
				className={cn(
					"h-6 min-w-[8rem] flex-1 bg-transparent px-1 text-ink-1 text-sm outline-none placeholder:text-ink-4",
					info.mono && "font-mono text-[13px]",
				)}
				placeholder={values.length === 0 ? info.placeholder : "Add another"}
				value={text}
				autoComplete="off"
				spellCheck={false}
				onChange={(event) => {
					const next = event.target.value;
					// A pick from the country list commits at once.
					if (country && next.includes(" · ") && parseCountry(next)) {
						commit(next);
						return;
					}
					setText(next);
				}}
				onKeyDown={onKeyDown}
				onPaste={onPaste}
				onBlur={() => {
					if (text.trim() !== "") commit(text);
				}}
			/>
		</div>
	);
}

/** A select for the field or operator: short, inline, the closed trigger shows the words. */
function InlineSelect({
	value,
	items,
	onChange,
	label,
	className,
}: {
	value: string;
	items: readonly { value: string; label: string }[];
	onChange(value: string): void;
	label: string;
	className?: string;
}) {
	return (
		<Select
			value={value}
			items={items}
			onValueChange={(next) => onChange(String(next ?? ""))}
		>
			<SelectTrigger size="sm" aria-label={label} className={className}>
				<SelectValue />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false}>
				{items.map((item) => (
					<SelectItem key={item.value} value={item.value}>
						{item.label}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}

const FIELD_ITEMS = FIELDS.map((f) => ({ value: f.value, label: f.label }));

/**
 * One condition, read as part of a sentence: "when / and" [field] [operator]
 * [name] on the first line, the values on a line of their own below (a list
 * of networks or paths needs the width).
 */
function ConditionRow({
	condition,
	index,
	ruleId,
	errors,
	onChange,
	onRemove,
	canRemove,
}: {
	condition: FirewallCondition;
	index: number;
	ruleId: string;
	errors: RuleErrors;
	onChange(next: FirewallCondition): void;
	onRemove(): void;
	canRemove: boolean;
}) {
	const id = useId();
	const info = fieldInfo(condition.field);
	const nameError = errors[`${ruleId}.${index}.name`];
	const valuesError = errors[`${ruleId}.${index}.values`];
	const ops = opChoices(condition.field).map((o) => ({
		value: o.key,
		label: o.label,
	}));
	const fieldLabel = `Condition ${index + 1}`;
	return (
		<li className="flex flex-col gap-1">
			<div className="flex flex-wrap items-start gap-1.5">
				<span
					aria-hidden
					className="w-9 shrink-0 pt-1.5 text-right font-mono text-2xs text-ink-4"
				>
					{index === 0 ? "when" : "and"}
				</span>
				<InlineSelect
					label={`${fieldLabel}: what to test`}
					value={condition.field}
					items={FIELD_ITEMS}
					onChange={(field) =>
						onChange(withField(condition, field as FirewallCondition["field"]))
					}
					className="w-[9.5rem]"
				/>
				{info.named ? (
					<Input
						aria-label={`${fieldLabel}: ${condition.field === "header" ? "header" : "parameter"} name`}
						aria-invalid={nameError ? true : undefined}
						aria-describedby={nameError ? `${id}-err` : undefined}
						className="h-7 w-[9rem] font-mono text-[13px]"
						placeholder={condition.field === "header" ? "X-Api-Key" : "debug"}
						value={condition.name ?? ""}
						autoComplete="off"
						spellCheck={false}
						onChange={(event) =>
							onChange({ ...condition, name: event.target.value })
						}
					/>
				) : null}
				<InlineSelect
					label={`${fieldLabel}: how to compare`}
					value={opKey(condition)}
					items={ops}
					onChange={(key) => onChange(withOp(condition, key))}
					className="w-[11rem]"
				/>
				<span className="flex-1" />
				<Button
					type="button"
					variant="ghost"
					size="icon-sm"
					aria-label={`Remove condition ${index + 1}`}
					disabled={!canRemove}
					onClick={onRemove}
				>
					<XIcon aria-hidden />
				</Button>
			</div>
			{condition.op === "present" ? null : (
				<div className="flex pl-[2.625rem]">
					<ValuesInput
						condition={condition}
						invalid={Boolean(valuesError)}
						describedBy={valuesError ? `${id}-err` : undefined}
						label={`${fieldLabel}: values`}
						onChange={(values) => onChange({ ...condition, values })}
					/>
				</div>
			)}
			{nameError || valuesError ? (
				<FieldError id={`${id}-err`} className="pl-[2.625rem]">
					{nameError ?? valuesError}
				</FieldError>
			) : null}
		</li>
	);
}

/**
 * Everything one rule says: its name, what it does and when. The conditions
 * read as one sentence ("when the path starts with /admin and the IP address
 * is not 10.0.0.0/8"), so the rule says what it does before it is applied.
 */
export function RuleEditor({
	rule,
	errors,
	onChange,
}: {
	rule: FirewallRule;
	errors: RuleErrors;
	onChange(next: FirewallRule): void;
}) {
	const id = useId();
	const conditions = rule.conditions ?? [];
	const nameError = errors[`${rule.id}.name`];
	const conditionsError = errors[`${rule.id}.conditions`];
	const setCondition = (index: number, next: FirewallCondition) =>
		onChange({
			...rule,
			conditions: conditions.map((c, i) => (i === index ? next : c)),
		});
	return (
		<div className="flex flex-col gap-4 pt-1 pb-2 pl-7">
			<div className="flex flex-wrap items-start gap-x-6 gap-y-3">
				<div className="flex min-w-[14rem] flex-1 flex-col gap-1.5">
					<Label htmlFor={`${id}-name`}>Name</Label>
					<Input
						id={`${id}-name`}
						value={rule.name}
						maxLength={160}
						autoComplete="off"
						aria-invalid={nameError ? true : undefined}
						aria-describedby={nameError ? `${id}-name-err` : undefined}
						onChange={(event) =>
							onChange({ ...rule, name: event.target.value })
						}
					/>
					<FieldError id={`${id}-name-err`}>{nameError}</FieldError>
				</div>
				<div className="flex flex-col gap-1.5">
					<span id={`${id}-action`} className="font-medium text-ink-1 text-sm">
						Then
					</span>
					<Segmented
						aria-label="What the rule does"
						font="sans"
						density="regular"
						value={rule.action}
						onValueChange={(action) => onChange({ ...rule, action })}
						options={[
							{ value: "block", label: "Block" },
							{ value: "allow", label: "Allow" },
						]}
					/>
					<FieldDescription className="max-w-[18rem]">
						{rule.action === "block"
							? "Matching requests get HTTP 403 and never reach the service."
							: "Matching requests go through, and the rules below are skipped for them."}
					</FieldDescription>
				</div>
			</div>
			<div className="flex flex-col gap-2">
				<div className="flex items-baseline gap-2">
					<span className="font-medium text-ink-1 text-sm">Conditions</span>
					<span className="text-ink-3 text-xs">
						A request matches when all of them do. Several values in one
						condition mean any of them.
					</span>
				</div>
				<ol className="m-0 flex list-none flex-col gap-2 p-0">
					{conditions.map((condition, index) => (
						<ConditionRow
							// Conditions have no id; their position is who they are.
							// biome-ignore lint/suspicious/noArrayIndexKey: see above
							key={index}
							ruleId={rule.id}
							index={index}
							condition={condition}
							errors={errors}
							canRemove={conditions.length > 1}
							onChange={(next) => setCondition(index, next)}
							onRemove={() =>
								onChange({
									...rule,
									conditions: conditions.filter((_, i) => i !== index),
								})
							}
						/>
					))}
				</ol>
				<FieldError>{conditionsError}</FieldError>
				<div className="pl-[2.625rem]">
					<Button
						type="button"
						variant="ghost"
						size="sm"
						disabled={conditions.length >= MAX_CONDITIONS}
						onClick={() =>
							onChange({
								...rule,
								conditions: [
									...conditions,
									{ field: "path", op: "prefix", values: [] },
								],
							})
						}
					>
						<PlusIcon aria-hidden />
						Add condition
					</Button>
				</div>
			</div>
		</div>
	);
}
