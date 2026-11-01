import { CalendarClockIcon, CheckIcon, ChevronDownIcon } from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import { Button } from "#/components/ui/button";
import { FieldError, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Popover,
	PopoverContent,
	PopoverTrigger,
} from "#/components/ui/popover";
import { Separator } from "#/components/ui/separator";
import {
	customRangeProblem,
	fromLocalInput,
	RANGE_PRESETS,
	type RangeSelection,
	rangeLabel,
	resolveRange,
	toLocalInput,
} from "./model";

export interface RangePickerProps {
	value: RangeSelection;
	onChange(next: RangeSelection): void;
}

/**
 * The time range of a stored history: five presets counted back from now and
 * a custom range of two `datetime-local` fields (the reader's own time zone;
 * the address keeps them as UTC instants). The trigger says the range in words.
 */
export function RangePicker({ value, onChange }: RangePickerProps) {
	const [open, setOpen] = useState(false);
	const [from, setFrom] = useState("");
	const [to, setTo] = useState("");
	const [problem, setProblem] = useState<string | null>(null);
	const fromId = useId();
	const toId = useId();

	// The fields start from the range on screen, so a custom range is an edit of it.
	const prepare = (next: boolean) => {
		if (next) {
			const shown = resolveRange(value, Date.now());
			setFrom(toLocalInput(shown.from));
			setTo(value.kind === "custom" ? toLocalInput(value.to) : "");
			setProblem(null);
		}
		setOpen(next);
	};

	const apply = (event: FormEvent) => {
		event.preventDefault();
		const start = fromLocalInput(from);
		const end = fromLocalInput(to);
		if (from !== "" && start === undefined) {
			setProblem("The start is not a date and time.");
			return;
		}
		if (to !== "" && end === undefined) {
			setProblem("The end is not a date and time.");
			return;
		}
		const wrong = customRangeProblem(start, end);
		if (wrong) {
			setProblem(wrong);
			return;
		}
		onChange({ kind: "custom", from: start, to: end });
		setOpen(false);
	};

	return (
		<Popover open={open} onOpenChange={prepare}>
			<PopoverTrigger
				render={
					<Button variant="secondary" size="sm" aria-label="Time range">
						<CalendarClockIcon aria-hidden />
						{rangeLabel(value)}
						<ChevronDownIcon aria-hidden className="text-ink-4" />
					</Button>
				}
			/>
			<PopoverContent align="start" className="w-[300px] gap-2.5 p-2">
				<fieldset className="m-0 flex min-w-0 flex-col border-0 p-0">
					<legend className="sr-only">Presets</legend>
					{RANGE_PRESETS.map((preset) => {
						const active = value.kind === "preset" && value.id === preset.id;
						return (
							<Button
								key={preset.id}
								variant="ghost"
								size="sm"
								aria-pressed={active}
								className="justify-between"
								onClick={() => {
									onChange({ kind: "preset", id: preset.id });
									setOpen(false);
								}}
							>
								{preset.label}
								{active ? <CheckIcon aria-hidden /> : null}
							</Button>
						);
					})}
				</fieldset>
				<Separator />
				<form onSubmit={apply} className="flex flex-col gap-2.5 px-1 pb-1">
					<span className="type-panel-title">Custom range</span>
					<div className="flex flex-col gap-1.5">
						<FieldLabel htmlFor={fromId}>From</FieldLabel>
						<Input
							id={fromId}
							type="datetime-local"
							value={from}
							onChange={(event) => setFrom(event.target.value)}
						/>
					</div>
					<div className="flex flex-col gap-1.5">
						<FieldLabel htmlFor={toId}>To (empty: now)</FieldLabel>
						<Input
							id={toId}
							type="datetime-local"
							value={to}
							onChange={(event) => setTo(event.target.value)}
						/>
					</div>
					{problem ? <FieldError>{problem}</FieldError> : null}
					<Button type="submit" variant="primary" size="sm">
						Apply range
					</Button>
				</form>
			</PopoverContent>
		</Popover>
	);
}
