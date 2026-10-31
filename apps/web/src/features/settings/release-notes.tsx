import { cn } from "cn";
import {
	type Block,
	type Inline,
	parseMarkdown,
} from "./release-notes-markdown";

function InlineView({ node }: { node: Inline }) {
	switch (node.type) {
		case "text":
			return node.value;
		case "code":
			return (
				<code className="type-mono rounded-sm bg-white/5 px-1">
					{node.value}
				</code>
			);
		case "strong":
			return (
				<strong className="font-semibold text-ink-1">
					<Inlines nodes={node.children} />
				</strong>
			);
		case "em":
			return (
				<em>
					<Inlines nodes={node.children} />
				</em>
			);
		case "link":
			return (
				<a
					href={node.href}
					target="_blank"
					rel="noopener noreferrer"
					className="text-accent-ink underline-offset-2 hover:underline"
				>
					<Inlines nodes={node.children} />
				</a>
			);
	}
}

function Inlines({ nodes }: { nodes: Inline[] }) {
	return nodes.map((node, index) => (
		// biome-ignore lint/suspicious/noArrayIndexKey: parsed text is positional and never reorders
		<InlineView key={index} node={node} />
	));
}

function BlockView({ block }: { block: Block }) {
	switch (block.type) {
		case "heading":
			return (
				<p className="m-0 font-medium text-ink-1">
					<Inlines nodes={block.children} />
				</p>
			);
		case "paragraph":
			return (
				<p className="m-0">
					<Inlines nodes={block.children} />
				</p>
			);
		case "quote":
			return (
				<blockquote className="m-0 border-line-strong border-l-2 pl-3 text-ink-3">
					<Inlines nodes={block.children} />
				</blockquote>
			);
		case "code":
			return (
				<pre className="surface-well m-0 overflow-x-auto whitespace-pre rounded-ctl p-2.5 font-mono text-2xs text-ink-2">
					{block.value}
				</pre>
			);
		case "list": {
			const List = block.ordered ? "ol" : "ul";
			return (
				<List
					className={cn(
						"m-0 flex flex-col gap-1 pl-5",
						block.ordered ? "list-decimal" : "list-disc",
					)}
				>
					{block.items.map((item, index) => (
						// biome-ignore lint/suspicious/noArrayIndexKey: parsed items are positional and never reorder
						<li key={index}>
							<Inlines nodes={item} />
						</li>
					))}
				</List>
			);
		}
	}
}

/**
 * A release's notes. The text is parsed into plain nodes and drawn as React
 * elements, so the notes can never carry HTML into the page, and only http(s)
 * links stay links.
 */
export function ReleaseNotes({
	markdown,
	label,
}: {
	markdown: string;
	/** Names the region for assistive tech: "Release notes for v0.1.2". */
	label: string;
}) {
	return (
		<section
			aria-label={label}
			className="flex flex-col gap-2.5 text-ink-2 text-xs leading-relaxed"
		>
			{parseMarkdown(markdown).map((block, index) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: parsed blocks are positional and never reorder
				<BlockView key={index} block={block} />
			))}
		</section>
	);
}
