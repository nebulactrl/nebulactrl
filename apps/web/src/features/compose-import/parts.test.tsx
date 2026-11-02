import { describe, expect, test } from "bun:test";
import {
	render,
	screen,
	waitForElementToBeRemoved,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComposeNote } from "#/api/gen/types.gen";
import { Disclosure, Group, NoteAlerts, ToggleRow } from "./parts";

describe("Group", () => {
	test("is a named region with a heading and its rows", () => {
		render(
			<Group title="Storage">
				<p>one volume</p>
			</Group>,
		);
		const region = screen.getByRole("region", { name: "Storage" });
		expect(
			within(region).getByRole("heading", { name: "Storage" }),
		).toBeInTheDocument();
		expect(within(region).getByText("one volume")).toBeInTheDocument();
	});
});

describe("ToggleRow", () => {
	test("the label names the switch and a click reports the next value", async () => {
		const user = userEvent.setup();
		const seen: boolean[] = [];
		render(
			<ToggleRow
				label="Public domain"
				description="Routes traffic from the internet."
				checked={false}
				onCheckedChange={(next) => seen.push(next)}
			/>,
		);
		const toggle = screen.getByRole("switch", { name: "Public domain" });
		expect(toggle).not.toBeChecked();
		expect(
			screen.getByText("Routes traffic from the internet."),
		).toBeInTheDocument();

		await user.click(toggle);
		expect(seen).toEqual([true]);
	});

	test("clicking the label text toggles it too", async () => {
		const user = userEvent.setup();
		const seen: boolean[] = [];
		render(
			<ToggleRow
				label="Public domain"
				checked
				onCheckedChange={(next) => seen.push(next)}
			/>,
		);
		await user.click(screen.getByText("Public domain"));
		expect(seen).toEqual([false]);
	});

	test("a disabled row ignores clicks and shows its extra content", async () => {
		const user = userEvent.setup();
		const seen: boolean[] = [];
		render(
			<ToggleRow
				label="Managed database"
				checked={false}
				disabled
				onCheckedChange={(next) => seen.push(next)}
			>
				<p>extra detail</p>
			</ToggleRow>,
		);
		const toggle = screen.getByRole("switch", { name: "Managed database" });
		expect(toggle).toHaveAttribute("data-disabled");
		await user.click(toggle);
		expect(seen).toEqual([]);
		expect(screen.getByText("extra detail")).toBeInTheDocument();
	});
});

describe("Disclosure", () => {
	test("starts closed, opens on click and closes again", async () => {
		const user = userEvent.setup();
		render(
			<Disclosure title="Networks" count={2}>
				<p>backend net</p>
			</Disclosure>,
		);
		const button = screen.getByRole("button", { name: /Networks/ });
		expect(button).toHaveAccessibleName("Networks (2)");
		expect(button).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByText("backend net")).not.toBeInTheDocument();

		await user.click(button);
		expect(button).toHaveAttribute("aria-expanded", "true");
		expect(screen.getByText("backend net")).toBeInTheDocument();

		await user.click(button);
		expect(button).toHaveAttribute("aria-expanded", "false");
		await waitForElementToBeRemoved(() => screen.queryByText("backend net"));
	});

	test("defaultOpen shows the body at once; no count shows no count", () => {
		render(
			<Disclosure title="Variables" defaultOpen>
				<p>FOO</p>
			</Disclosure>,
		);
		expect(screen.getByRole("button", { name: "Variables" })).toHaveAttribute(
			"aria-expanded",
			"true",
		);
		expect(screen.getByText("FOO")).toBeInTheDocument();
	});
});

describe("NoteAlerts", () => {
	const notes: ComposeNote[] = [
		{ key: "deploy.resources", message: "Limits were read.", severity: "info" },
		{ key: "network_mode", message: "Host networking.", severity: "warning" },
		{ key: "privileged", message: "Privileged mode.", severity: "danger" },
	];

	test("dangers come before warnings; info notes fold into a disclosure", async () => {
		const user = userEvent.setup();
		render(<NoteAlerts notes={notes} />);
		const alerts = screen.getAllByRole("note");
		expect(alerts.map((a) => a.textContent)).toEqual([
			"privilegedPrivileged mode.",
			"network_modeHost networking.",
		]);

		const info = screen.getByRole("button", { name: "Ignored settings (1)" });
		expect(screen.queryByText(/Limits were read/)).not.toBeInTheDocument();
		await user.click(info);
		const item = screen.getByText(/Limits were read/).closest("li");
		expect(item).toHaveTextContent("deploy.resources: Limits were read.");
	});

	test("the disclosure takes the caller's title; nothing renders for no notes", () => {
		const { container, rerender } = render(
			<NoteAlerts
				notes={[{ key: "x", message: "m", severity: "info" }]}
				ignoredTitle="Ignored project settings"
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Ignored project settings (1)" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("note")).not.toBeInTheDocument();

		rerender(<NoteAlerts notes={null} />);
		expect(container).toBeEmptyDOMElement();
		rerender(<NoteAlerts notes={undefined} />);
		expect(container).toBeEmptyDOMElement();
	});
});
