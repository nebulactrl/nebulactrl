import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { DitherSparkline } from "@nebulactrl/dither/charts/sparkline";
import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import type { Service } from "#/api/models";
import { KpiNumber } from "#/components/common/kpi-number";
import { serviceMetricsQuery } from "#/features/metrics/queries";
import { formatBytes } from "#/lib/format";
import { cpuFigure, memoryFigure, type ResourceFigure } from "./model";

const SPARK_H = 34;

/**
 * The last hour as a dithered sparkline. The scale starts at 0 and tops out
 * at `ceiling` (or the series' own peak when that is higher), so a flat 3%
 * line stays flat instead of filling the box.
 */
function Spark({
	values,
	ceiling,
	color,
	label,
}: {
	values: number[];
	ceiling: number;
	color: string;
	label: string;
}) {
	const domain = useMemo(
		() => [0, Math.max(ceiling, ...values, Number.EPSILON)] as const,
		[values, ceiling],
	);
	if (values.length < 2) {
		return (
			<p className="grid h-[34px] place-items-center text-2xs text-ink-4">
				Collecting samples…
			</p>
		);
	}
	return (
		<DitherSparkline
			data={values}
			label={label}
			color={color}
			height={SPARK_H}
			domain={domain}
			className="w-full"
		/>
	);
}

function ResourceTile({
	title,
	figure,
	values,
	ceiling,
	color,
}: {
	title: string;
	figure: ResourceFigure;
	values: number[];
	ceiling: number;
	color: string;
}) {
	return (
		<div className="surface-raised flex min-w-0 flex-col gap-2 rounded-card px-3 pt-2.5 pb-3">
			<span className="type-micro">{title}</span>
			<span className="type-metric truncate text-ink-1">
				<KpiNumber text={figure.headline} />
			</span>
			<p
				title={figure.detail}
				className="truncate font-mono text-[10.5px] text-ink-3 leading-none"
			>
				{figure.detail}
			</p>
			<Spark
				values={values}
				ceiling={ceiling}
				color={color}
				label={`${title} over the last hour`}
			/>
			{figure.percent !== null ? (
				<DitherProgressBar
					hideLabel
					size="sm"
					label={`${title} of limit`}
					value={figure.percent}
					valueText={`${Math.round(figure.percent)}%`}
					color={color}
				/>
			) : null}
		</div>
	);
}

/**
 * Resources: CPU and memory now, with the last hour as a sparkline. Real
 * samples only: a service with none yet says so instead of drawing a line.
 */
export function ResourcesCard({ service }: { service: Service }) {
	const metrics = useQuery({
		...serviceMetricsQuery(service.id, "1h"),
		enabled: service.currentDeploymentId !== null,
	});
	const points = metrics.data?.points ?? [];
	const newest = points[points.length - 1];

	const cpu = cpuFigure(service, newest);
	const memory = memoryFigure(service, newest, formatBytes);
	const cpuSeries = useMemo(() => points.map((p) => p.cpuPercent), [points]);
	const memSeries = useMemo(() => points.map((p) => p.memBytes), [points]);
	const cpuLimit =
		service.config.cpus && newest
			? service.config.cpus * Math.max(1, newest.replicaCount) * 100
			: 0;
	const memLimit =
		newest && newest.memLimitBytes > 0 ? newest.memLimitBytes : 0;

	return (
		<div className="grid grid-cols-2 gap-2.5">
			<ResourceTile
				title="CPU"
				figure={cpu}
				values={cpuSeries}
				ceiling={cpuLimit}
				color="var(--color-accent-ink)"
			/>
			<ResourceTile
				title="Memory"
				figure={memory}
				values={memSeries}
				ceiling={memLimit}
				color="var(--color-info)"
			/>
		</div>
	);
}
