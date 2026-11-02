import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { EventMatrix } from "./event-matrix";
import type { Severity } from "./model";
import { EVENT_TYPES } from "./test-fixtures";

function Harness({
	initial,
	onChange,
	onSeverity,
}: {
	initial: string[];
	onChange?: (next: string[]) => void;
	onSeverity?: (next: Severity) => void;
}) {
	const [selected, setSelected] = useState(initial);
	const [severity, setSeverity] = useState<Severity>("info");
	return (
		<EventMatrix
			types={EVENT_TYPES}
			selected={selected}
			onSelectedChange={(next) => {
				onChange?.(next);
				setSelected(next);
			}}
			minSeverity={severity}
			onMinSeverityChange={(next) => {
				onSeverity?.(next);
				setSeverity(next);
			}}
		/>
	);
}

describe("EventMatrix", () => {
	test("groups the event types and counts the selected ones per group", () => {
		render(<Harness initial={["deploy.failed"]} />);
		const deployments = screen.getByRole("group", { name: "Deployments" });
		expect(within(deployments).getByText("1/2")).toBeInTheDocument();
		expect(
			within(deployments).getByRole("checkbox", { name: /Deployment failed/ }),
		).toBeChecked();
		expect(
			within(deployments).getByRole("checkbox", {
				name: /Deployment succeeded/,
			}),
		).not.toBeChecked();
		const servers = screen.getByRole("group", { name: "Servers" });
		expect(within(servers).getByText("0/1")).toBeInTheDocument();
	});

	test("shows each type's description and default severity", () => {
		render(<Harness initial={[]} />);
		expect(screen.getByText("A server stopped answering.")).toBeInTheDocument();
		expect(screen.getByText("warning")).toHaveAttribute(
			"title",
			"Default severity: warning",
		);
	});

	test("ticking a type reports the selection in catalogue order", async () => {
		const seen: string[][] = [];
		const user = userEvent.setup();
		render(
			<Harness initial={["server.offline"]} onChange={(n) => seen.push(n)} />,
		);
		await user.click(
			screen.getByRole("checkbox", { name: /Deployment failed/ }),
		);
		expect(seen.at(-1)).toEqual(["deploy.failed", "server.offline"]);
		await user.click(screen.getByRole("checkbox", { name: /Server offline/ }));
		expect(seen.at(-1)).toEqual(["deploy.failed"]);
	});

	test("a group's button selects every type of that group, then clears it", async () => {
		const seen: string[][] = [];
		const user = userEvent.setup();
		render(
			<Harness initial={["server.offline"]} onChange={(n) => seen.push(n)} />,
		);
		const deployments = screen.getByRole("group", { name: "Deployments" });
		await user.click(
			within(deployments).getByRole("button", { name: "Select all" }),
		);
		expect(seen.at(-1)).toEqual([
			"deploy.failed",
			"deploy.succeeded",
			"server.offline",
		]);
		expect(within(deployments).getByText("2/2")).toBeInTheDocument();
		await user.click(
			within(deployments).getByRole("button", { name: "Clear" }),
		);
		expect(seen.at(-1)).toEqual(["server.offline"]);
	});

	test("presets select the defaults, everything or nothing, and the matching chip is pressed", async () => {
		const seen: string[][] = [];
		const user = userEvent.setup();
		render(<Harness initial={[]} onChange={(n) => seen.push(n)} />);
		const presets = screen.getByRole("group", { name: "Events" });
		const nothing = within(presets).getByRole("button", { name: "Nothing" });
		const alerts = within(presets).getByRole("button", {
			name: "Failures and alerts",
		});
		const all = within(presets).getByRole("button", { name: "Everything" });
		expect(nothing).toHaveAttribute("aria-pressed", "true");
		await user.click(alerts);
		expect(seen.at(-1)).toEqual(["deploy.failed", "server.offline"]);
		expect(alerts).toHaveAttribute("aria-pressed", "true");
		expect(nothing).toHaveAttribute("aria-pressed", "false");
		await user.click(all);
		expect(seen.at(-1)).toEqual([
			"deploy.failed",
			"deploy.succeeded",
			"server.offline",
		]);
		expect(all).toHaveAttribute("aria-pressed", "true");
	});

	test("a hand-picked selection highlights no preset", () => {
		render(<Harness initial={["deploy.succeeded"]} />);
		const presets = screen.getByRole("group", { name: "Events" });
		for (const name of ["Failures and alerts", "Everything", "Nothing"]) {
			expect(within(presets).getByRole("button", { name })).toHaveAttribute(
				"aria-pressed",
				"false",
			);
		}
	});

	test("the minimum severity can be changed", async () => {
		const seen: Severity[] = [];
		const user = userEvent.setup();
		render(<Harness initial={[]} onSeverity={(s) => seen.push(s)} />);
		const group = screen.getByRole("group", { name: "Minimum severity" });
		expect(
			within(group).getByRole("button", { name: "Everything" }),
		).toHaveAttribute("aria-pressed", "true");
		await user.click(
			within(group).getByRole("button", { name: "Errors only" }),
		);
		expect(seen).toEqual(["error"]);
		expect(
			within(group).getByRole("button", { name: "Errors only" }),
		).toHaveAttribute("aria-pressed", "true");
	});
});
