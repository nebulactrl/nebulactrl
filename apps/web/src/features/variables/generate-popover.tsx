import { DicesIcon } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "#/components/ui/button";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Popover,
	PopoverContent,
	PopoverTrigger,
} from "#/components/ui/popover";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import {
	ALPHABETS,
	type Alphabet,
	DEFAULT_GENERATOR,
	ENCODINGS,
	type Encoding,
	type GeneratorKind,
	type GeneratorOptions,
	generatesSecret,
	generatorExpression,
	generatorProblem,
	generatorShape,
} from "./generator";

const KINDS: readonly { value: GeneratorKind; label: string }[] = [
	{ value: "secret", label: "Secret" },
	{ value: "randomBytes", label: "Bytes" },
	{ value: "uuid", label: "UUID" },
	{ value: "randomInt", label: "Number" },
];

/**
 * Builds a generator expression such as `${{secret(32)}}` and hands it to the
 * value field. The expression is only text: the control plane generates the
 * value once, when the variable is saved, and never returns it. The popover
 * shows the shape of what will be generated, never a sample.
 */
export function GeneratePopover({
	onInsert,
	disabled,
}: {
	onInsert(expression: string, makesSecret: boolean): void;
	disabled?: boolean;
}) {
	const [open, setOpen] = useState(false);
	const [options, setOptions] = useState<GeneratorOptions>(DEFAULT_GENERATOR);
	const id = useId();
	const set = <K extends keyof GeneratorOptions>(
		key: K,
		value: GeneratorOptions[K],
	) => setOptions((current) => ({ ...current, [key]: value }));
	const problem = generatorProblem(options);

	return (
		<Popover open={open} onOpenChange={setOpen}>
			<PopoverTrigger
				render={
					<Button
						type="button"
						variant="secondary"
						size="md"
						disabled={disabled ?? false}
					/>
				}
			>
				<DicesIcon aria-hidden />
				Generate
			</PopoverTrigger>
			<PopoverContent align="end" className="w-[320px]">
				<Segmented
					aria-label="What to generate"
					font="sans"
					value={options.kind}
					onValueChange={(kind) => set("kind", kind)}
					options={KINDS}
				/>
				{options.kind === "secret" || options.kind === "randomBytes" ? (
					<Field>
						<FieldLabel htmlFor={`${id}-length`}>
							{options.kind === "secret" ? "Length" : "Bytes"}
						</FieldLabel>
						<Input
							id={`${id}-length`}
							type="number"
							inputMode="numeric"
							min={1}
							max={1024}
							value={options.length}
							onChange={(event) => set("length", Number(event.target.value))}
						/>
					</Field>
				) : null}
				{options.kind === "secret" ? (
					<Field>
						<FieldLabel htmlFor={`${id}-alphabet`}>Characters</FieldLabel>
						<Select
							value={options.alphabet}
							items={ALPHABETS.map((a) => ({ value: a.id, label: a.label }))}
							onValueChange={(next) => set("alphabet", next as Alphabet)}
						>
							<SelectTrigger id={`${id}-alphabet`} className="w-full">
								<SelectValue />
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false}>
								{ALPHABETS.map((a) => (
									<SelectItem key={a.id} value={a.id}>
										{a.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Field>
				) : null}
				{options.kind === "randomBytes" ? (
					<Field>
						<FieldLabel>Encoding</FieldLabel>
						<Segmented
							aria-label="Encoding"
							value={options.encoding}
							onValueChange={(encoding) =>
								set("encoding", encoding as Encoding)
							}
							options={ENCODINGS.map((value) => ({ value, label: value }))}
						/>
					</Field>
				) : null}
				{options.kind === "randomInt" ? (
					<div className="grid grid-cols-2 gap-2">
						<Field>
							<FieldLabel htmlFor={`${id}-min`}>Minimum</FieldLabel>
							<Input
								id={`${id}-min`}
								type="number"
								inputMode="numeric"
								value={options.min}
								onChange={(event) => set("min", Number(event.target.value))}
							/>
						</Field>
						<Field>
							<FieldLabel htmlFor={`${id}-max`}>Maximum</FieldLabel>
							<Input
								id={`${id}-max`}
								type="number"
								inputMode="numeric"
								value={options.max}
								onChange={(event) => set("max", Number(event.target.value))}
							/>
						</Field>
					</div>
				) : null}
				{problem ? (
					<FieldError>{problem}</FieldError>
				) : (
					<FieldDescription>
						Generates {generatorShape(options)}, once, when you save.
						{generatesSecret(options.kind)
							? " The variable becomes a secret: its value is never shown."
							: ""}
					</FieldDescription>
				)}
				<Button
					type="button"
					disabled={problem !== null}
					onClick={() => {
						onInsert(
							generatorExpression(options),
							generatesSecret(options.kind),
						);
						setOpen(false);
					}}
				>
					Insert
				</Button>
			</PopoverContent>
		</Popover>
	);
}
