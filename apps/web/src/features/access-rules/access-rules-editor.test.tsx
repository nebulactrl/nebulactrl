import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { renderWithProviders } from "#/test/render";
import {
	AccessRulesEditor,
	type AccessRulesEditorProps,
} from "./access-rules-editor";

function Host({
	allow: allow0 = [],
	deny: deny0 = [],
	...rest
}: {
	allow?: string[];
	deny?: string[];
} & Partial<Omit<AccessRulesEditorProps, "allow" | "deny">>) {
	const [allow, setAllow] = useState(allow0);
	const [deny, setDeny] = useState(deny0);
	return (
		<AccessRulesEditor
			allow={allow}
			deny={deny}
			onAllowChange={setAllow}
			onDenyChange={setDeny}
			{...rest}
		/>
	);
}

const preview = () =>
	within(screen.getByRole("region", { name: "Effective rules" }))
		.getAllByRole("listitem")
		.map((li) => li.textContent);

describe("AccessRulesEditor", () => {
	test("an empty pair lets everyone in and says how to restrict it", async () => {
		await renderWithProviders(<Host />);
		expect(screen.getByText("Allowed networks")).toBeInTheDocument();
		expect(screen.getByText("Denied networks")).toBeInTheDocument();
		expect(
			screen.getByText(
				/Leave it empty to allow everyone except the denied networks/,
			),
		).toBeInTheDocument();
		expect(preview()).toEqual(["1Everything else: allowed"]);
	});

	test("the preview lists denies, then allows, then the default, in canonical form", async () => {
		await renderWithProviders(
			<Host allow={["192.168.1.20", "10.1.2.3/8"]} deny={["172.16.0.0/12"]} />,
		);
		expect(preview()).toEqual([
			"1Deny 172.16.0.0/12",
			"2Allow 192.168.1.20",
			"3Allow 10.0.0.0/8",
			"4Everything else: denied",
		]);
		// counts show beside the list titles
		expect(
			screen.getByText("Allowed networks").parentElement,
		).toHaveTextContent("2");
		expect(screen.getByText("Denied networks").parentElement).toHaveTextContent(
			"1",
		);
	});

	test("adding a network in each list updates the preview", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Host />);
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText("Allowed network 1"), "10.0.0.0/8");
		await user.click(
			screen.getByRole("button", { name: "Add denied network" }),
		);
		await user.type(screen.getByLabelText("Denied network 1"), "10.9.0.0/16");
		expect(preview()).toEqual([
			"1Deny 10.9.0.0/16",
			"2Allow 10.0.0.0/8",
			"3Everything else: denied",
		]);
	});

	test("an invalid entry is named under its row", async () => {
		await renderWithProviders(<Host allow={["10.0.0.0/8", "banana"]} />);
		expect(
			screen.getByText("“banana” is not a CIDR or IP address."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Allowed network 2")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	test("rules that deny every address are called out", async () => {
		await renderWithProviders(
			<Host allow={["10.0.0.0/8"]} deny={["10.0.0.0/8"]} />,
		);
		expect(
			screen.getByText(
				"The rules deny every address. Remove a denied network or add an allowed one.",
			),
		).toBeInTheDocument();
	});

	test("a required allow list that is empty gives guidance, not an error", async () => {
		await renderWithProviders(<Host allowRequired />);
		expect(
			screen.getByText("Only these networks can connect."),
		).toBeInTheDocument();
		expect(screen.queryByText(/Leave it empty/)).toBeNull();
		expect(
			screen.getByText(
				"Add at least one allowed network. Nothing can connect until you do.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Add at least one allowed network.")).toBeNull();
		expect(preview()).toEqual(["1Everything else: denied"]);
	});

	test("a custom empty hint replaces the default guidance", async () => {
		await renderWithProviders(
			<Host allowRequired allowEmptyHint="Add your office network." />,
		);
		expect(screen.getByText("Add your office network.")).toBeInTheDocument();
		expect(screen.queryByText(/Nothing can connect until you do/)).toBeNull();
	});

	test("a caller's own check overrides the computed one, with per-list keys", async () => {
		await renderWithProviders(
			<Host
				allow={["10.0.0.0/8"]}
				allowKey="a"
				denyKey="d"
				check={{
					entries: { "a.0": "Allow row is wrong." },
					allow: "Too many allowed.",
					deny: "Too many denied.",
					rules: "Pair is wrong.",
				}}
			/>,
		);
		for (const text of [
			"Allow row is wrong.",
			"Too many allowed.",
			"Too many denied.",
			"Pair is wrong.",
		]) {
			expect(screen.getByText(text)).toBeInTheDocument();
		}
	});

	test("the note, custom labels and hidden preview are honoured", async () => {
		await renderWithProviders(
			<Host
				allow={["10.0.0.0/8"]}
				note="TCP cannot tell hostnames apart."
				allowLabel="Office network"
				denyLabel="Blocked network"
				hidePreview
			/>,
		);
		expect(screen.getByRole("note")).toHaveTextContent(
			"TCP cannot tell hostnames apart.",
		);
		expect(screen.getByLabelText("Office network 1")).toHaveValue("10.0.0.0/8");
		expect(
			screen.getByRole("list", { name: "Blocked network" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("region", { name: "Effective rules" }),
		).toBeNull();
	});

	test("disabled freezes both lists", async () => {
		await renderWithProviders(
			<Host allow={["10.0.0.0/8"]} deny={["10.9.0.0/16"]} disabled />,
		);
		expect(screen.getByLabelText("Allowed network 1")).toBeDisabled();
		expect(screen.getByLabelText("Denied network 1")).toBeDisabled();
		expect(screen.getByRole("button", { name: "Add network" })).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Add denied network" }),
		).toBeDisabled();
	});
});
