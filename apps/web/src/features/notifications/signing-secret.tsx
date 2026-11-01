import { useState } from "react";
import { CodeBlock } from "#/components/common/code-block";
import { CopyButton } from "#/components/common/copy-button";
import { Badge } from "#/components/ui/badge";
import { Segmented } from "#/components/ui/segmented";
import { SNIPPET_LABEL, type SnippetLang, VERIFY_SNIPPETS } from "./model";

/** How a receiver verifies a delivery, in three languages. */
function VerifySnippets() {
	const [lang, setLang] = useState<SnippetLang>("node");
	return (
		<div className="flex flex-col gap-2">
			<div className="flex flex-wrap items-center justify-between gap-2">
				<span className="type-label">Verify a delivery</span>
				<Segmented<SnippetLang>
					aria-label="Language"
					font="sans"
					value={lang}
					onValueChange={setLang}
					options={(Object.keys(SNIPPET_LABEL) as SnippetLang[]).map((l) => ({
						value: l,
						label: SNIPPET_LABEL[l],
					}))}
				/>
			</div>
			<CodeBlock copyValue={VERIFY_SNIPPETS[lang]}>
				<pre className="m-0 max-h-64 overflow-auto overscroll-contain whitespace-pre font-mono text-2xs leading-relaxed">
					{VERIFY_SNIPPETS[lang]}
				</pre>
			</CodeBlock>
			<p className="text-ink-3 text-xs leading-relaxed">
				Every request carries{" "}
				<code className="type-mono text-ink-2">X-NebulaCtrl-Signature</code>:{" "}
				<code className="type-mono text-ink-2">
					t=&lt;unix seconds&gt;,v1=&lt;hex&gt;
				</code>
				, an HMAC-SHA256 of{" "}
				<code className="type-mono text-ink-2">t + "." + body</code> keyed with
				the signing secret. Hash the raw bytes you received, compare in constant
				time and reject timestamps more than five minutes off.{" "}
				<code className="type-mono text-ink-2">X-NebulaCtrl-Event</code> and{" "}
				<code className="type-mono text-ink-2">X-NebulaCtrl-Delivery</code> name
				the event type and the delivery.
			</p>
		</div>
	);
}

/** A webhook's signing secret, shown once, with the verification code. */
export function SigningSecretPanel({ secret }: { secret: string }) {
	return (
		<div className="flex flex-col gap-4">
			<div className="flex items-center gap-2">
				<Badge tone="warn" shape="capsule">
					Shown once
				</Badge>
				<span className="text-ink-3 text-xs">
					NebulaCtrl keeps it encrypted and never shows it again. Rotate it to
					get a new one.
				</span>
			</div>
			<div className="surface-well flex items-start gap-3 rounded-ctl py-2.5 pr-2.5 pl-3.5">
				<code
					data-testid="signing-secret"
					className="min-w-0 flex-1 select-all break-all py-1 font-mono text-ink-1 text-xs leading-[1.7]"
				>
					{secret}
				</code>
				<CopyButton variant="chip" value={secret} className="shrink-0" />
			</div>
			<VerifySnippets />
		</div>
	);
}
