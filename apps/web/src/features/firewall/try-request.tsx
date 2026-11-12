import { cn } from "cn";
import { ArrowRightIcon, BanIcon, ShieldCheckIcon } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import { Label } from "#/components/ui/label";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Textarea } from "#/components/ui/textarea";
import {
	countryName,
	type Decision,
	decide,
	type FirewallRule,
	METHODS,
	parseCountry,
	type TestRequest,
} from "./model";
import { COUNTRY_LIST_ID } from "./rule-editor";

const START: TestRequest = {
	method: "GET",
	target: "/",
	ip: "",
	country: "",
	userAgent: "",
	headers: "",
};

const METHOD_ITEMS = METHODS.map((m) => ({ value: m, label: m }));

/**
 * A request, described by hand, and what the rules as edited do with it. It
 * answers as you type, before anything is applied, with the same order and
 * matching the proxy uses.
 */
export function TryRequest({
	rules,
	onShowRule,
	countryKnown,
}: {
	rules: readonly FirewallRule[];
	/** Opens the rule that decided. */
	onShowRule(index: number): void;
	/** The server places visitors by country; without it country rules wait. */
	countryKnown: boolean;
}) {
	const id = useId();
	const [req, setReq] = useState<TestRequest>(START);
	const [countryText, setCountryText] = useState("");
	const decision = useMemo(() => decide(rules, req), [rules, req]);
	const set = (patch: Partial<TestRequest>) =>
		setReq((r) => ({ ...r, ...patch }));

	return (
		<div className="flex flex-col gap-3">
			<div className="grid grid-cols-[7rem_1fr] gap-3">
				<div className="flex flex-col gap-1.5">
					<Label htmlFor={`${id}-method`}>Method</Label>
					<Select
						value={req.method}
						items={METHOD_ITEMS}
						onValueChange={(m) => set({ method: String(m ?? "GET") })}
					>
						<SelectTrigger id={`${id}-method`} className="font-mono">
							<SelectValue />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							{METHOD_ITEMS.map((m) => (
								<SelectItem key={m.value} value={m.value} className="font-mono">
									{m.label}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</div>
				<div className="flex flex-col gap-1.5">
					<Label htmlFor={`${id}-path`}>Path</Label>
					<Input
						id={`${id}-path`}
						className="font-mono"
						value={req.target}
						placeholder="/admin/login?next=/"
						autoComplete="off"
						spellCheck={false}
						onChange={(e) => set({ target: e.target.value })}
					/>
				</div>
			</div>
			<div className="grid grid-cols-1 gap-3 @lg/section:grid-cols-3">
				<div className="flex flex-col gap-1.5">
					<Label htmlFor={`${id}-ip`}>IP address</Label>
					<Input
						id={`${id}-ip`}
						className="font-mono"
						value={req.ip}
						placeholder="203.0.113.7"
						autoComplete="off"
						spellCheck={false}
						onChange={(e) => set({ ip: e.target.value })}
					/>
				</div>
				<div className="flex flex-col gap-1.5">
					<Label htmlFor={`${id}-country`}>Country</Label>
					<Input
						id={`${id}-country`}
						list={COUNTRY_LIST_ID}
						value={countryText}
						placeholder="Any"
						autoComplete="off"
						onChange={(e) => {
							const text = e.target.value;
							const code = parseCountry(text);
							setCountryText(
								code && text.includes(" · ")
									? `${code} · ${countryName(code)}`
									: text,
							);
							set({ country: code ?? "" });
						}}
					/>
				</div>
				<div className="flex flex-col gap-1.5">
					<Label htmlFor={`${id}-ua`}>User agent</Label>
					<Input
						id={`${id}-ua`}
						value={req.userAgent}
						placeholder="Mozilla/5.0 …"
						autoComplete="off"
						spellCheck={false}
						onChange={(e) => set({ userAgent: e.target.value })}
					/>
				</div>
			</div>
			<details className="group">
				<summary className="w-fit cursor-pointer text-ink-3 text-xs hover:text-ink-1 focus-visible:focus-ring">
					Headers
				</summary>
				<Textarea
					aria-label="Request headers, one Name: value per line"
					className="mt-1.5 font-mono text-[13px]"
					rows={3}
					placeholder={"Authorization: Bearer …\nX-Forwarded-Proto: https"}
					value={req.headers}
					onChange={(e) => set({ headers: e.target.value })}
				/>
			</details>
			<Verdict
				decision={decision}
				onShowRule={onShowRule}
				countryNote={!countryKnown && req.country !== ""}
			/>
		</div>
	);
}

function Verdict({
	decision,
	onShowRule,
	countryNote,
}: {
	decision: Decision;
	onShowRule(index: number): void;
	countryNote: boolean;
}) {
	const blocked = decision.kind === "block";
	const Icon = blocked
		? BanIcon
		: decision.kind === "allow"
			? ShieldCheckIcon
			: ArrowRightIcon;
	return (
		<output
			aria-live="polite"
			className={cn(
				"surface-well flex min-h-11 flex-wrap items-center gap-x-2.5 gap-y-1 rounded-ctl px-3 py-2 text-[13px]",
			)}
		>
			<Icon
				aria-hidden
				className={cn(
					"size-4 shrink-0",
					blocked
						? "text-bad"
						: decision.kind === "allow"
							? "text-ok"
							: "text-ink-3",
				)}
			/>
			{decision.kind === "pass" ? (
				<span className="text-ink-2">
					<span className="font-medium text-ink-1">Reaches the service.</span>{" "}
					No rule matches it.
				</span>
			) : (
				<>
					<span className="min-w-0 text-ink-2">
						<span className="font-medium text-ink-1">
							{blocked ? "Blocked" : "Allowed"} by rule {decision.index + 1}
						</span>
						, {decision.rule.name}.{" "}
						{blocked
							? "The visitor gets HTTP 403."
							: "The rules below it are skipped."}
					</span>
					<Button
						type="button"
						variant="link"
						size="sm"
						className="ml-auto text-xs"
						onClick={() => onShowRule(decision.index)}
					>
						Show rule
					</Button>
				</>
			)}
			{countryNote ? (
				<span className="basis-full text-ink-3 text-xs">
					On this server country rules wait for geolocation; until then the
					proxy skips them.
				</span>
			) : null}
		</output>
	);
}
