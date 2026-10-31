import { cn } from "cn";
import { type ComponentType, type ReactNode, useState } from "react";
import {
	type ApiOperation,
	type ApiParam,
	type ApiSchema,
	type ApiTagPage,
	type DocMeta,
	docHref,
	operationAnchor,
	schemaLabel,
} from "./types.ts";

/**
 * How a link to another docs page is drawn. The in-app docs pass a router link
 * so navigation stays client side; the public site passes a plain anchor.
 */
export type DocLinkComponent = ComponentType<{
	slug: string;
	className?: string;
	children: ReactNode;
}>;

/** A plain anchor to `/docs/<slug>`: what a static page uses. */
export const PlainDocLink: DocLinkComponent = ({
	slug,
	className,
	children,
}) => (
	<a href={docHref(slug)} className={className}>
		{children}
	</a>
);

/** The tag cards under the API reference index page. */
export function ApiTagList({
	tags,
	Link = PlainDocLink,
}: {
	tags: DocMeta[];
	Link?: DocLinkComponent;
}) {
	return (
		<div className="mt-6 grid gap-2 sm:grid-cols-2">
			{tags.map((tag) => (
				<Link
					key={tag.slug}
					slug={tag.slug}
					className="surface-raised block rounded-card px-3.5 py-3 no-underline outline-none hover:bg-surface-float focus-visible:focus-ring"
				>
					<span className="block font-medium text-[13px] text-ink-1">
						{tag.title}
					</span>
					<span className="mt-0.5 block text-[13px] text-ink-3 leading-snug">
						{tag.description}
					</span>
				</Link>
			))}
		</div>
	);
}

/** One tag's operations: the body of an API reference page. */
export function ApiTagView({
	page,
	title,
	Link = PlainDocLink,
}: {
	page: ApiTagPage;
	title: string;
	Link?: DocLinkComponent;
}) {
	return (
		<div>
			<h1 className="m-0 mb-2 font-display font-semibold text-[28px] text-ink-1 leading-[1.15] tracking-[-0.02em]">
				{title} API
			</h1>
			{page.description ? (
				<p className="m-0 max-w-[72ch] text-[14px] text-ink-2">
					{page.description}.
				</p>
			) : null}
			<p className="mt-2 max-w-[72ch] text-[13px] text-ink-3 leading-relaxed">
				{page.operations.length}{" "}
				{page.operations.length === 1 ? "operation" : "operations"}. Every call
				needs a session cookie or an{" "}
				<code className="font-mono text-ink-2">
					Authorization: Bearer nct_…
				</code>{" "}
				token unless marked Public.{" "}
				<Link slug="reference/api" className="text-accent-ink">
					Conventions, errors and scopes
				</Link>
			</p>
			<div className="mt-6 flex flex-col gap-4">
				{page.operations.map((op) => (
					<OperationCard key={op.id} op={op} defs={page.defs} />
				))}
			</div>
		</div>
	);
}

function OperationCard({
	op,
	defs,
}: {
	op: ApiOperation;
	defs: Record<string, ApiSchema>;
}) {
	const successful = op.responses.filter((r) => /^[23]/.test(r.status));
	const failures = op.responses.filter((r) => /^[45]/.test(r.status));
	return (
		<section
			id={operationAnchor(op)}
			className="surface-raised scroll-mt-6 rounded-card px-4 py-3.5"
		>
			<header className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
				<span className="docs-method" data-method={op.method}>
					{op.method}
				</span>
				<code className="break-all font-mono text-[13px] text-ink-1">
					{op.path}
				</code>
				{op.public ? (
					<span className="ml-auto rounded-tip bg-status-live-wash px-1.5 py-px text-2xs text-status-live-on">
						Public
					</span>
				) : null}
			</header>
			{op.summary ? (
				<h2 className="mt-2 mb-0 font-semibold text-[14px] text-ink-1">
					{op.summary}
				</h2>
			) : null}
			{op.description ? (
				<p className="mt-1.5 mb-0 max-w-[72ch] text-[13px] text-ink-2 leading-relaxed">
					<InlineCode text={op.description} />
				</p>
			) : null}

			{op.params.length > 0 ? <ParamTable params={op.params} /> : null}

			{op.body ? (
				<Disclosure
					title={`Request body · ${op.body.contentType}${op.body.required ? " · required" : ""}`}
				>
					<SchemaTree schema={op.body.schema} defs={defs} />
				</Disclosure>
			) : null}

			{successful.map((response) => (
				<Disclosure
					key={response.status}
					title={`Response ${response.status} · ${response.description}${response.contentType ? ` · ${response.contentType}` : ""}`}
				>
					{response.schema ? (
						<SchemaTree schema={response.schema} defs={defs} />
					) : (
						<p className="m-0 text-[13px] text-ink-3">No body.</p>
					)}
				</Disclosure>
			))}

			{failures.length > 0 ? (
				<div className="mt-3 text-[13px]">
					<div className="type-micro mb-1.5">Errors</div>
					<ul className="m-0 flex list-none flex-col gap-1 p-0">
						{failures.map((response) => {
							const codes = op.errorCodes.filter(
								(c) => c.status === response.status,
							);
							return (
								<li key={response.status} className="flex flex-wrap gap-x-2">
									<code className="font-mono text-ink-1">
										{response.status}
									</code>
									<span className="text-ink-3">{response.description}</span>
									{codes.map((c) => (
										<code
											key={c.code}
											className="rounded-tip bg-surface-well px-1.5 font-mono text-ink-2"
										>
											{c.code}
										</code>
									))}
								</li>
							);
						})}
					</ul>
					<p className="mt-1.5 mb-0 text-ink-4">
						Errors are RFC 9457{" "}
						<code className="font-mono">application/problem+json</code> with a
						stable <code className="font-mono">code</code> field.
					</p>
				</div>
			) : null}
		</section>
	);
}

/** Description text with `backticked` spans drawn as code. */
function InlineCode({ text }: { text: string }) {
	const parts = text.split(/(`[^`]+`)/g);
	let offset = 0;
	return parts.map((part) => {
		// Offset and length: a leading empty part and the part after it share an offset.
		const key = `${offset}:${part.length}`;
		offset += part.length;
		return part.startsWith("`") && part.endsWith("`") && part.length > 2 ? (
			<code
				key={key}
				className="rounded-tip bg-surface-well px-1 font-mono text-[13px] text-ink-1 [overflow-wrap:anywhere]"
			>
				{part.slice(1, -1)}
			</code>
		) : (
			<span key={key}>{part}</span>
		);
	});
}

function ParamTable({ params }: { params: ApiParam[] }) {
	return (
		<div className="mt-3 overflow-x-auto">
			<table className="w-full border-collapse text-[13px]">
				<thead>
					<tr className="text-left text-ink-3">
						<th className="py-1 pr-3 font-medium">Parameter</th>
						<th className="py-1 pr-3 font-medium">In</th>
						<th className="py-1 pr-3 font-medium">Type</th>
						<th className="py-1 font-medium">Description</th>
					</tr>
				</thead>
				<tbody>
					{params.map((param) => (
						<tr
							key={`${param.in}:${param.name}`}
							className="border-line-faint border-t align-top"
						>
							<td className="py-1.5 pr-3">
								<code className="font-mono text-ink-1">{param.name}</code>
								{param.required ? (
									<span className="ml-1.5 text-2xs text-status-staged-on">
										required
									</span>
								) : null}
							</td>
							<td className="py-1.5 pr-3 text-ink-3">{param.in}</td>
							<td className="py-1.5 pr-3 font-mono text-ink-3">{param.type}</td>
							<td className="py-1.5 text-ink-3">{param.description}</td>
						</tr>
					))}
				</tbody>
			</table>
		</div>
	);
}

/** A collapsed section: native `<details>`, so it needs no state and works from the keyboard. */
function Disclosure({
	title,
	children,
}: {
	title: string;
	children: ReactNode;
}) {
	return (
		<details className="docs-details mt-3 border-line-faint border-t pt-2.5">
			<summary className="text-[13px] text-ink-2 outline-none focus-visible:focus-ring">
				{title}
			</summary>
			<div className="mt-2">{children}</div>
		</details>
	);
}

/** The expandable members of a schema, or nothing for a scalar. */
function members(
	schema: ApiSchema,
	defs: Record<string, ApiSchema>,
): ApiSchema | null {
	const resolved = schema.ref ? defs[schema.ref] : schema;
	if (!resolved) return null;
	if (resolved.type === "array" && resolved.items)
		return members(resolved.items, defs) ?? null;
	if (schema.type === "array" && schema.items)
		return members(schema.items, defs);
	if (resolved.props || resolved.variants || resolved.additional)
		return resolved;
	return null;
}

function SchemaTree({
	schema,
	defs,
}: {
	schema: ApiSchema;
	defs: Record<string, ApiSchema>;
}) {
	const open = members(schema, defs);
	return (
		<div className="font-mono text-[13px]">
			<div className="mb-1 text-ink-3">{schemaLabel(schema)}</div>
			{open ? <SchemaBody schema={open} defs={defs} /> : null}
		</div>
	);
}

function SchemaBody({
	schema,
	defs,
}: {
	schema: ApiSchema;
	defs: Record<string, ApiSchema>;
}) {
	return (
		<ul className="m-0 flex list-none flex-col gap-1 border-line-faint border-l pl-3">
			{schema.props?.map((prop) => (
				<SchemaRow
					key={prop.name}
					name={prop.name}
					required={prop.required}
					schema={prop.schema}
					defs={defs}
				/>
			))}
			{schema.additional ? (
				<SchemaRow
					name="[key]"
					required={false}
					schema={schema.additional}
					defs={defs}
				/>
			) : null}
			{schema.variants?.map((variant, i) => (
				<SchemaRow
					// biome-ignore lint/suspicious/noArrayIndexKey: fixed list
					key={i}
					name={`option ${i + 1}`}
					required={false}
					schema={variant}
					defs={defs}
				/>
			))}
		</ul>
	);
}

/**
 * One property. A nested object or a named schema expands on click; children
 * are only rendered while open, so a self-referencing schema cannot recurse.
 */
function SchemaRow({
	name,
	required,
	schema,
	defs,
}: {
	name: string;
	required: boolean;
	schema: ApiSchema;
	defs: Record<string, ApiSchema>;
}) {
	const [open, setOpen] = useState(false);
	const inner = members(schema, defs);
	const description =
		schema.description ??
		(schema.ref ? defs[schema.ref]?.description : undefined);
	return (
		<li>
			<div className="flex flex-wrap items-baseline gap-x-2">
				{inner ? (
					<button
						type="button"
						aria-expanded={open}
						onClick={() => setOpen((v) => !v)}
						className={cn(
							"cursor-pointer text-ink-1 outline-none focus-visible:focus-ring pointer-coarse:py-2.5",
							open ? "underline decoration-dotted underline-offset-2" : "",
						)}
					>
						{open ? "▾" : "▸"} {name}
					</button>
				) : (
					<span className="text-ink-1">{name}</span>
				)}
				<span className="text-ink-3">{schemaLabel(schema)}</span>
				{required ? (
					<span className="text-2xs text-status-staged-on">required</span>
				) : null}
			</div>
			{description ? (
				<div className="font-sans text-ink-4 leading-snug">{description}</div>
			) : null}
			{open && inner ? (
				<div className="mt-1">
					<SchemaBody schema={inner} defs={defs} />
				</div>
			) : null}
		</li>
	);
}
