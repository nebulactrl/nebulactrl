import { CheckIcon } from "lucide-react";
import type { ValidateVariableResult } from "#/api/gen/types.gen";
import { ToneIcon } from "#/components/common/tone-icon";
import { FieldError } from "#/components/ui/field";

/**
 * What the server's dry run found in a draft value: a syntax error with its
 * position, warnings (weak generators, invisible characters) and references
 * that do not resolve yet. Quiet when everything is fine.
 */
export function DraftFeedback({
	result,
}: {
	result: ValidateVariableResult | null;
}) {
	if (result === null) return null;
	if (!result.ok) {
		return (
			<FieldError>
				{result.error
					? `${result.error.message} (character ${result.error.offset + 1})`
					: (result.message ?? "This value cannot be used.")}
			</FieldError>
		);
	}
	const warnings = result.warnings ?? [];
	const unresolved = (result.references ?? []).filter((r) => !r.resolved);
	const resolved = (result.references ?? []).filter((r) => r.resolved);
	if (warnings.length === 0 && unresolved.length === 0 && resolved.length === 0)
		return null;
	return (
		<ul aria-live="polite" className="flex flex-col gap-1 text-[11.5px]">
			{warnings.map((warning) => (
				<li
					key={`${warning.code}-${warning.message}`}
					className="flex items-start gap-1.5 text-warn"
				>
					<ToneIcon tone="warning" className="mt-px size-3.5" />
					{warning.message}
				</li>
			))}
			{unresolved.map((ref) => (
				<li
					key={`${ref.ns ?? ""}.${ref.key}`}
					className="flex items-start gap-1.5 text-warn"
				>
					<ToneIcon tone="warning" className="mt-px size-3.5" />
					{ref.message ||
						`${ref.ns ? `${ref.ns}.` : ""}${ref.key} does not resolve yet`}
					. A deploy is blocked until it does.
				</li>
			))}
			{resolved.map((ref) => (
				<li
					key={`${ref.ns ?? ""}.${ref.key}`}
					className="flex items-center gap-1.5 font-mono text-ink-3"
				>
					<CheckIcon aria-hidden className="size-3.5 shrink-0 text-ok" />
					{ref.ns ? `${ref.ns}.` : ""}
					{ref.key}
					{ref.secret ? " · contains a secret" : ""}
				</li>
			))}
		</ul>
	);
}
