import { useQuery } from "@tanstack/react-query";
import { CloudIcon } from "lucide-react";
import { useId } from "react";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { applyZone, matchZone } from "#/features/cloudflare/model";
import {
	cloudflareQuery,
	cloudflareZonesQuery,
} from "#/features/cloudflare/queries";

/**
 * Next to the hostname field of a new domain: pick one of the Cloudflare zones
 * to build the hostname under, and see whether the typed hostname falls into
 * one (its DNS record is then created for you). Renders nothing without a
 * Cloudflare connection.
 */
export function ZonePicker({
	hostname,
	disabled,
	onPick,
}: {
	hostname: string;
	disabled: boolean;
	onPick(hostname: string): void;
}) {
	const id = useId();
	const connection = useQuery(cloudflareQuery());
	const configured = connection.data?.configured === true;
	const zones = useQuery({ ...cloudflareZonesQuery(), enabled: configured });
	const list = zones.data?.items ?? [];
	if (!configured || list.length === 0) return null;

	const match = matchZone(hostname, list);
	const typed = hostname.trim() !== "";

	return (
		<div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-ink-3 text-xs">
			<CloudIcon aria-hidden className="size-4 shrink-0" />
			<label htmlFor={id} className="text-ink-2">
				Zone
			</label>
			<Select
				value={match?.name ?? null}
				items={list.map((zone) => ({ value: zone.name, label: zone.name }))}
				disabled={disabled}
				onValueChange={(next) => {
					if (next) onPick(applyZone(hostname, next));
				}}
			>
				<SelectTrigger
					id={id}
					aria-label="Cloudflare zone"
					className="min-w-[12rem]"
				>
					<SelectValue
						placeholder={typed ? "No matching zone" : "Pick a zone…"}
					/>
				</SelectTrigger>
				<SelectContent alignItemWithTrigger={false}>
					{list.map((zone) => (
						<SelectItem key={zone.id} value={zone.name}>
							{zone.name}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<span aria-live="polite" className="min-w-0 flex-1">
				{!typed
					? "Pick a zone to build the hostname, or type one."
					: match
						? `In ${match.name}: NebulaCtrl creates its DNS record and keeps it in step. It starts ${hostname.trim().startsWith("*.") ? "DNS only" : "proxied"}.`
						: "In none of your Cloudflare zones: point DNS yourself."}
			</span>
		</div>
	);
}
