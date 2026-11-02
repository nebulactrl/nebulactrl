import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import {
	ImpactBadge,
	IncidentStatusBadge,
	PhaseBadge,
	StateLabel,
} from "./badges";
import {
	IMPACT_LABEL,
	INCIDENT_IMPACTS,
	INCIDENT_STATUSES,
	type PageState,
	PHASE_LABEL,
	STATE_LABEL,
	STATUS_LABEL,
} from "./model";

describe("ImpactBadge", () => {
	it.each(INCIDENT_IMPACTS)("names the %s impact in words", (impact) => {
		render(<ImpactBadge impact={impact} />);
		expect(screen.getByText(IMPACT_LABEL[impact])).toBeInTheDocument();
	});

	it("gives an impactful incident a different treatment than 'none'", () => {
		const none = render(<ImpactBadge impact="none" />);
		const noneClass = none.container.firstElementChild?.className;
		none.unmount();
		const critical = render(<ImpactBadge impact="critical" />);
		expect(critical.container.firstElementChild?.className).not.toBe(noneClass);
	});
});

describe("IncidentStatusBadge", () => {
	it.each(INCIDENT_STATUSES)("names the %s status", (status) => {
		render(<IncidentStatusBadge status={status} />);
		expect(screen.getByText(STATUS_LABEL[status])).toBeInTheDocument();
	});

	it("marks resolved apart from an open status", () => {
		const open = render(<IncidentStatusBadge status="investigating" />);
		const openClass = open.container.firstElementChild?.className;
		open.unmount();
		const resolved = render(<IncidentStatusBadge status="resolved" />);
		expect(resolved.container.firstElementChild?.className).not.toBe(openClass);
	});
});

describe("PhaseBadge", () => {
	it.each(Object.keys(PHASE_LABEL) as (keyof typeof PHASE_LABEL)[])(
		"names the %s phase",
		(phase) => {
			render(<PhaseBadge phase={phase} />);
			expect(screen.getByText(PHASE_LABEL[phase])).toBeInTheDocument();
		},
	);

	it("highlights the running phase", () => {
		const upcoming = render(<PhaseBadge phase="upcoming" />);
		const upcomingClass = upcoming.container.firstElementChild?.className;
		upcoming.unmount();
		const active = render(<PhaseBadge phase="active" />);
		expect(active.container.firstElementChild?.className).not.toBe(
			upcomingClass,
		);
	});
});

describe("StateLabel", () => {
	it.each(Object.keys(STATE_LABEL) as PageState[])(
		"reads the %s state",
		(state) => {
			render(<StateLabel state={state} />);
			expect(screen.getByText(STATE_LABEL[state])).toBeInTheDocument();
		},
	);

	it("passes a class name to the wrapper", () => {
		const { container } = render(
			<StateLabel state="operational" className="mt-9" />,
		);
		expect(container.firstElementChild).toHaveClass("mt-9");
	});
});
