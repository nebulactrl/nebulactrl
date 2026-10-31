import { DitherStatCard } from "@nebulactrl/dither/charts/stat-card";

const wave = (seed: number, drift: number) =>
	Array.from({ length: 30 }, (_, i) =>
		Math.round(
			60 +
				18 * Math.sin(i / 2.6 + seed) +
				8 * Math.cos(i / 1.4 + seed) +
				i * drift,
		),
	);

export function StatCardDemo() {
	return (
		<section className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-4">
			<h3 className="type-micro">Stat card</h3>
			<div className="grid grid-cols-2 gap-2.5 lg:grid-cols-3">
				<DitherStatCard
					label="Requests"
					value="1.2M"
					delta={{ text: "▲ 12%", tone: "good", title: "vs previous 24h" }}
					hint="vs previous 24h"
					trend={wave(0.4, 0.8)}
					color="ink"
				/>
				<DitherStatCard
					label="Visitors"
					value="48.3K"
					delta={{ text: "▼ 3%", tone: "bad", title: "vs previous 24h" }}
					hint="per UTC day"
					trend={wave(1.2, -0.3)}
					color="ok"
				/>
				<DitherStatCard
					label="Bandwidth"
					value="312 GB"
					delta={{ text: "● 0%", tone: "neutral" }}
					trend={wave(2.1, 0.1)}
					color="info"
				/>
				<DitherStatCard
					label="p95 latency"
					value="184 ms"
					delta={{ text: "▲ 8%", tone: "bad" }}
					trend={wave(3.3, 0.5)}
					color="warn"
				/>
				<DitherStatCard
					label="Active users"
					value={12_480}
					delta={8.1}
					deltaLabel="vs last week"
					trend={wave(0.9, 0.6)}
					color="blue"
				/>
				<DitherStatCard
					label="Error rate"
					value="0.42%"
					delta={-31.2}
					good="down"
					deltaLabel="vs last week"
					trend={wave(1.7, -0.4)}
					color="green"
					fill="dotted"
				/>
				<DitherStatCard
					label="Open incidents"
					value={0}
					delta={0}
					deltaLabel="no change"
				/>
			</div>
		</section>
	);
}
