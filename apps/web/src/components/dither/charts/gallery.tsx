import { AreaChartDemo } from "./demos/area-chart-demo";
import { BarChartDemo } from "./demos/bar-chart-demo";
import { CartesianChartDemo } from "./demos/cartesian-chart-demo";
import { DonutChartDemo } from "./demos/donut-chart-demo";
import { GaugeDemo } from "./demos/gauge-demo";
import { HeatmapDemo } from "./demos/heatmap-demo";
import { LineChartDemo } from "./demos/line-chart-demo";
import { ProgressBarDemo } from "./demos/progress-bar-demo";
import { RadarChartDemo } from "./demos/radar-chart-demo";
import { SparklineDemo } from "./demos/sparkline-demo";
import { StatCardDemo } from "./demos/stat-card-demo";
import { UptimeBarDemo } from "./demos/uptime-bar-demo";

/**
 * Every chart in the kit with its knobs. The dev-only `/dev/dither` route
 * renders it; it is also the screenshot surface for the group.
 */
export function ChartsGallery() {
	return (
		<div className="@container flex flex-col gap-4">
			<StatCardDemo />
			<div className="grid gap-4 @4xl:grid-cols-2">
				<AreaChartDemo />
				<BarChartDemo />
			</div>
			<LineChartDemo />
			<CartesianChartDemo />
			<div className="grid gap-4 @4xl:grid-cols-2">
				<SparklineDemo />
				<ProgressBarDemo />
			</div>
			<HeatmapDemo />
			<UptimeBarDemo />
			<div className="grid gap-4 @4xl:grid-cols-3">
				<DonutChartDemo />
				<RadarChartDemo />
				<GaugeDemo />
			</div>
		</div>
	);
}
