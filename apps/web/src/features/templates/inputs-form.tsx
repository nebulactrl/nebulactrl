import { useState } from "react";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import {
	type InputValues,
	inputHint,
	inputValue,
	type TemplateInput,
} from "./lib";

/** The browser's own field for the input's format: it validates and brings the right keyboard. */
function fieldType(
	input: TemplateInput,
): "password" | "email" | "url" | "text" {
	if (input.secret) return "password";
	if (input.format === "email" || input.format === "url") return input.format;
	return "text";
}

function fieldInputMode(input: TemplateInput): "numeric" | undefined {
	return input.format === "number" ? "numeric" : undefined;
}

function InputField({
	id,
	input,
	value,
	onChange,
}: {
	id: string;
	input: TemplateInput;
	value: string;
	onChange(value: string): void;
}) {
	const [touched, setTouched] = useState(false);
	const empty = value.trim() === "";
	const invalid = touched && empty && input.required && !input.secret;
	const hint = inputHint(input);
	const options = input.options ?? [];
	return (
		<Field>
			<FieldLabel htmlFor={id}>
				{input.label}
				{input.required ? (
					<>
						<span aria-hidden className="text-destructive-text">
							*
						</span>
						<span className="sr-only">(required)</span>
					</>
				) : null}
			</FieldLabel>
			{options.length > 0 ? (
				<Select
					value={value}
					items={options.map((option) => ({ value: option, label: option }))}
					onValueChange={(next) => onChange(String(next ?? ""))}
				>
					<SelectTrigger id={id} className="w-full">
						<SelectValue placeholder="Choose one" />
					</SelectTrigger>
					<SelectContent alignItemWithTrigger={false}>
						{options.map((option) => (
							<SelectItem key={option} value={option}>
								{option}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			) : (
				<Input
					id={id}
					type={fieldType(input)}
					inputMode={fieldInputMode(input)}
					scale="lg"
					autoComplete={input.secret ? "new-password" : "off"}
					spellCheck={false}
					required={input.required}
					aria-invalid={invalid}
					aria-describedby={invalid ? `${id}-error` : undefined}
					placeholder={input.secret && hint ? "Generated" : undefined}
					value={value}
					onChange={(event) => onChange(event.target.value)}
					onBlur={() => setTouched(true)}
					className={input.secret ? "font-mono" : undefined}
				/>
			)}
			{input.description ? (
				<FieldDescription>{input.description}</FieldDescription>
			) : null}
			{hint ? <FieldDescription>{hint}</FieldDescription> : null}
			{invalid ? (
				<FieldError id={`${id}-error`}>{input.label} is required.</FieldError>
			) : null}
		</Field>
	);
}

/**
 * The install form, from the template's own inputs: secrets are password
 * fields that say they are generated when left empty, inputs with options are
 * selects, and required ones are marked.
 */
export function InputsForm({
	inputs,
	typed,
	onChange,
	idPrefix,
}: {
	inputs: readonly TemplateInput[];
	typed: InputValues;
	onChange(key: string, value: string): void;
	idPrefix: string;
}) {
	if (inputs.length === 0) return null;
	return (
		<div className="flex flex-col gap-3.5">
			{inputs.map((input) => (
				<InputField
					key={input.key}
					id={`${idPrefix}-${input.key}`}
					input={input}
					value={inputValue(input, typed)}
					onChange={(value) => onChange(input.key, value)}
				/>
			))}
		</div>
	);
}
