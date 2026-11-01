import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { Badge } from "#/components/ui/badge";
import { compactNumber } from "#/features/metrics/chart-format";
import { statusClass } from "../lists";

const CLASS_COLOR = {
	"2xx": "var(--color-status-live)",
	"3xx": "var(--color-info)",
	"4xx": "var(--color-warn)",
	"5xx": "var(--color-bad)",
	other: "var(--color-status-stopped)",
} as const;

const CLASS_TONE = {
	"2xx": "ok",
	"3xx": "info",
	"4xx": "warn",
	"5xx": "bad",
	other: "off",
} as const;

const REASON: Record<number, string> = {
	200: "OK",
	201: "Created",
	204: "No content",
	206: "Partial content",
	301: "Moved permanently",
	302: "Found",
	304: "Not modified",
	307: "Temporary redirect",
	308: "Permanent redirect",
	400: "Bad request",
	401: "Unauthorized",
	403: "Forbidden",
	404: "Not found",
	405: "Method not allowed",
	408: "Request timeout",
	429: "Too many requests",
	499: "Client closed request",
	500: "Internal server error",
	502: "Bad gateway",
	503: "Service unavailable",
	504: "Gateway timeout",
};

export interface StatusCodeListProps {
	codes: readonly { code: number; n: number }[] | null | undefined;
	limit?: number;
}

/**
 * Exact status codes, biggest first: a class-toned chip with the code in text,
 * the class and reason beside it, the count and share, and a bar in an
 * engraved groove. The tint is a second cue only; "4xx" and the reason say
 * the same thing in words.
 */
export function StatusCodeList({ codes, limit = 8 }: StatusCodeListProps) {
	const rows = [...(codes ?? [])]
		.filter((c) => c.n > 0)
		.sort((a, b) => b.n - a.n)
		.slice(0, limit);
	if (rows.length === 0) {
		return (
			<p className="px-1 py-3 text-ink-3 text-xs">
				No responses in this range.
			</p>
		);
	}
	const max = rows[0]?.n ?? 1;
	const total = (codes ?? []).reduce((sum, c) => sum + c.n, 0);
	const classParts = (["2xx", "3xx", "4xx", "5xx", "other"] as const)
		.map((key) => {
			const sum = (codes ?? [])
				.filter((c) => c.n > 0 && statusClass(c.code) === key)
				.reduce((acc, c) => acc + c.n, 0);
			return {
				label: key,
				value: sum,
				color: CLASS_COLOR[key],
				text: compactNumber(sum),
			};
		})
		.filter((part) => part.value > 0);
	return (
		<div className="flex flex-col gap-3">
			<DitherProgressBar
				label="Responses by status class"
				hideLabel
				parts={classParts}
				max={total}
				size="md"
			/>
			<ul className="flex flex-col gap-0.5">
				{rows.map(({ code, n }) => {
					const cls = statusClass(code);
					const share = total > 0 ? (n / total) * 100 : 0;
					return (
						<li
							key={code}
							className="flex flex-col gap-1.5 rounded-ctl px-2 py-1.5"
						>
							<span className="flex items-center gap-2">
								<Badge
									tone={CLASS_TONE[cls]}
									className="w-[52px] shrink-0 justify-start font-semibold text-2xs"
								>
									{code === 0 ? "none" : code}
								</Badge>
								<span className="min-w-0 flex-1 truncate text-ink-3 text-xs">
									<span className="font-mono text-2xs">{cls}</span>{" "}
									{REASON[code] ?? ""}
								</span>
								<span className="w-12 shrink-0 text-right font-medium font-mono text-ink-1 text-xs tabular-nums">
									{compactNumber(n)}
								</span>
								<span className="w-10 shrink-0 text-right font-mono text-2xs text-ink-3 tabular-nums">
									{share >= 10 ? share.toFixed(0) : share.toFixed(1)}%
								</span>
							</span>
							<span aria-hidden className="block w-full">
								<DitherProgressBar
									inline
									hideLabel
									size="sm"
									label={`${code === 0 ? "none" : code}`}
									value={Math.max((n / max) * 100, 2)}
									color={CLASS_COLOR[cls]}
								/>
							</span>
						</li>
					);
				})}
			</ul>
		</div>
	);
}
