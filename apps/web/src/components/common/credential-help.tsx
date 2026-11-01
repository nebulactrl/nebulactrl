import { cn } from "cn";
import { CheckIcon, ExternalLinkIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { type CredentialGuide, firstEmptyId } from "#/lib/credential-links";
import { CodeBlock } from "./code-block";

function readFieldValue(id: string): string | null {
	const el = document.getElementById(id);
	return el instanceof HTMLInputElement ? el.value : null;
}

/**
 * Coming back from the provider's tab, put the cursor in the first empty
 * field of the form. It only listens after the button was used, so switching
 * tabs for any other reason never moves focus.
 */
function useFocusOnReturn(armed: boolean, fieldIds: readonly string[]) {
	useEffect(() => {
		if (!armed) return;
		const onVisible = () => {
			if (document.visibilityState !== "visible") return;
			const id = firstEmptyId(fieldIds, readFieldValue);
			if (id !== null) document.getElementById(id)?.focus();
		};
		document.addEventListener("visibilitychange", onVisible);
		return () => document.removeEventListener("visibilitychange", onVisible);
	}, [armed, fieldIds]);
}

/**
 * Where to create a token and what it needs, above the token field: a button
 * that opens the provider (with the permissions already selected where the
 * provider allows it), the permissions as a checklist, a policy to copy where
 * the provider takes one, and what to do next. `fieldIds` are the inputs the
 * credential goes into, in order; the first empty one is focused when the
 * person returns.
 */
export function CredentialHelp({
	guide,
	fieldIds,
	className,
}: {
	guide: CredentialGuide;
	fieldIds: readonly string[];
	className?: string;
}) {
	const [opened, setOpened] = useState(false);
	useFocusOnReturn(opened, fieldIds);

	const heading = guide.prefilled
		? "Selected for you"
		: guide.policy
			? "Allowed by the policy"
			: "Select these";

	return (
		<section
			aria-label={`${guide.provider} credential setup`}
			className={cn(
				"surface-well flex flex-col gap-3 rounded-ctl p-3",
				className,
			)}
		>
			{guide.cta ? (
				<div className="flex flex-col items-start gap-1.5">
					{guide.url ? (
						<Button
							variant="secondary"
							size="sm"
							nativeButton={false}
							onClick={() => setOpened(true)}
							render={
								<a href={guide.url} target="_blank" rel="noopener noreferrer" />
							}
						>
							<ExternalLinkIcon aria-hidden />
							{guide.cta}
						</Button>
					) : (
						<Button variant="secondary" size="sm" disabled>
							<ExternalLinkIcon aria-hidden />
							{guide.cta}
						</Button>
					)}
					{guide.url === null && guide.whyNoLink ? (
						<span className="text-ink-3 text-xs">{guide.whyNoLink}</span>
					) : null}
				</div>
			) : null}
			<div className="flex flex-col gap-1.5">
				<h5 className="type-label m-0">{heading}</h5>
				<ul className="m-0 flex list-none flex-col gap-1 p-0 text-ink-2 text-xs">
					{guide.permissions.map((p) => (
						<li key={p.label} className="flex items-baseline gap-2">
							<CheckIcon
								aria-hidden
								className="size-3 shrink-0 translate-y-0.5 text-ink-3"
							/>
							<span className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
								<span className="font-mono text-[12px] text-ink-1">
									{p.label}
								</span>
								{p.optional ? <Badge>optional</Badge> : null}
								{p.why ? <span className="text-ink-3">{p.why}</span> : null}
							</span>
						</li>
					))}
				</ul>
			</div>
			{guide.policy ? (
				<div className="flex flex-col gap-1.5">
					<h5 className="type-label m-0">{guide.policy.title}</h5>
					<CodeBlock copyValue={guide.policy.text}>
						{guide.policy.text}
					</CodeBlock>
				</div>
			) : null}
			{guide.note ? (
				<p className="m-0 text-ink-3 text-xs leading-relaxed">{guide.note}</p>
			) : null}
			<p className="m-0 flex flex-wrap items-baseline gap-x-2 text-ink-2 text-xs">
				<span>{guide.paste}</span>
				<Button
					variant="link"
					nativeButton={false}
					render={
						<a href={guide.docs} target="_blank" rel="noopener noreferrer" />
					}
				>
					{guide.provider} docs
				</Button>
			</p>
		</section>
	);
}
