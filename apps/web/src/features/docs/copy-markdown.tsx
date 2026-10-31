import { useEffect, useRef, useState } from "react";
import { COPIED_MS, writeClipboard } from "#/components/common/copy-button";
import { CopyCheckIcon } from "#/components/icons/animated/copy-check-icon";
import { TextMorph } from "#/components/motion/text-morph";
import { Button } from "#/components/ui/button";

type State = "idle" | "busy" | "copied" | "failed";

/** Fetches a page's raw markdown (`/docs/<slug>.md`) and puts it on the clipboard. */
async function copyMarkdown(slug: string): Promise<boolean> {
	const url = `/docs/${slug}.md`;
	const read = async () => {
		const response = await fetch(url);
		if (!response.ok) throw new Error(`${url}: ${response.status}`);
		return response.text();
	};
	try {
		// Safari only allows the write inside the click if the text is a promise.
		if (typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
			const blob = read().then(
				(text) => new Blob([text], { type: "text/plain" }),
			);
			await navigator.clipboard.write([
				new ClipboardItem({ "text/plain": blob }),
			]);
			return true;
		}
		return await writeClipboard(await read());
	} catch {
		return false;
	}
}

/** "Copy page as Markdown": for pasting a page into an AI assistant. */
export function CopyMarkdownButton({ slug }: { slug: string }) {
	const [state, setState] = useState<State>("idle");
	const timer = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(timer.current), []);

	const onClick = async () => {
		setState("busy");
		const ok = await copyMarkdown(slug);
		setState(ok ? "copied" : "failed");
		window.clearTimeout(timer.current);
		timer.current = window.setTimeout(() => setState("idle"), COPIED_MS * 1.6);
	};

	const label =
		state === "copied"
			? "Copied as Markdown"
			: state === "failed"
				? "Could not copy"
				: "Copy page as Markdown";
	return (
		<Button
			type="button"
			variant="secondary"
			size="sm"
			disabled={state === "busy"}
			onClick={onClick}
		>
			<CopyCheckIcon
				copied={state === "copied"}
				checkClassName="text-status-live-on"
			/>
			<TextMorph text={label} />
			<span className="sr-only" role="status" aria-live="polite">
				{state === "copied" ? "Page copied as Markdown" : ""}
			</span>
		</Button>
	);
}
