import { useQuery } from "@tanstack/react-query";
import { serverUnrestrictedQuery } from "#/features/stacks/queries";
import type { ServerView } from "./model";

/** What the host reports beyond the Overview's strip: kernel, cgroup, agent protocol, the proxy's ports and the Docker Compose plugin. Ruled rows, a fixed label column and the value beside it. */
export function HostDetails({ server }: { server: ServerView }) {
	// Compose support is read from the same endpoint as the Unrestricted
	// switch; a caller that may not read it simply gets no row.
	const compose = useQuery(serverUnrestrictedQuery(server.id)).data;
	const facts = server.facts;
	const rows: [string, string][] = [
		[
			"Protocol",
			server.protocolVersion === null ? "–" : `v${server.protocolVersion}`,
		],
	];
	if (facts) {
		rows.push(
			["Kernel", facts.kernel],
			["cgroup", `v${facts.cgroupVersion}`],
			[
				"Ports 80 / 443",
				`${facts.port80Free ? "free" : "in use"} / ${facts.port443Free ? "free" : "in use"}`,
			],
		);
	}
	if (compose) {
		rows.push([
			"Compose plugin",
			compose.composeVersion === ""
				? "not installed, so stacks cannot be deployed here"
				: `${compose.composeVersion}${compose.available ? ", stacks can run here" : ", too old for stacks (2.24 or newer needed)"}`,
		]);
	}
	return (
		<dl className="rule-rows max-w-[720px]">
			{rows.map(([label, value]) => (
				<div
					key={label}
					className="grid min-w-0 grid-cols-[minmax(96px,160px)_minmax(0,1fr)] items-baseline gap-x-4"
				>
					<dt className="type-label">{label}</dt>
					<dd className="min-w-0 truncate text-left font-mono text-ink-1 text-xs tabular-nums">
						<span title={value}>{value}</span>
					</dd>
				</div>
			))}
		</dl>
	);
}
