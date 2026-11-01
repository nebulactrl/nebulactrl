import { useId } from "react";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { type DomainMode, normalizeSuffix, previewHostname } from "./model";

const MODES = [
	{ value: "drop", label: "No domains" },
	{ value: "suffix", label: "Suffix the names" },
] as const;

/**
 * Domains of a clone. Hostnames are unique, so a clone never keeps its
 * source's: it gets none (attach new ones later) or the same names with a
 * suffix on the first label.
 */
export function DomainsField({
	mode,
	onMode,
	suffix,
	onSuffix,
	placeholder,
	example,
}: {
	mode: DomainMode;
	onMode(mode: DomainMode): void;
	suffix: string;
	onSuffix(suffix: string): void;
	/** The suffix used when the field is empty, for example `-copy`. */
	placeholder: string;
	/** A hostname of the source, to show the result on. */
	example?: string | undefined;
}) {
	const id = useId();
	const effective = normalizeSuffix(suffix) || placeholder;
	const shown = example ? previewHostname(example, effective) : null;
	return (
		<Field>
			<FieldLabel htmlFor={id}>Domains</FieldLabel>
			<Segmented
				aria-label="Domains of the clone"
				value={mode}
				onValueChange={onMode}
				options={MODES}
				font="sans"
				density="regular"
			/>
			{mode === "suffix" ? (
				<>
					<Input
						id={id}
						autoComplete="off"
						spellCheck={false}
						placeholder={placeholder}
						value={suffix}
						onChange={(event) => onSuffix(event.target.value)}
					/>
					<FieldDescription>
						Added to the first label of each hostname
						{shown && example ? (
							<>
								: <span className="font-mono">{example}</span> becomes{" "}
								<span className="font-mono">{shown}</span>
							</>
						) : null}
						. A name that is already taken stops the clone.
					</FieldDescription>
				</>
			) : (
				<FieldDescription>
					The clone starts without domains: attach its own in the Networking
					settings.
				</FieldDescription>
			)}
		</Field>
	);
}
