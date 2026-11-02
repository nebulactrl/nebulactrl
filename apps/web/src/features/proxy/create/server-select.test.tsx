import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { routeServers, ServerSelect } from "./server-select";
import { server } from "./test-fixtures";

const edge = server({ id: "s-edge", name: "edge" });
const old = server({
	id: "s-old",
	name: "legacy",
	proxy: { v2: false },
});
const down = server({ id: "s-down", name: "backup", status: "offline" });
const revoked = server({ id: "s-rev", name: "gone", status: "revoked" });
const pending = server({ id: "s-pend", name: "new", status: "pending" });

describe("routeServers", () => {
	test("keeps enrolled servers, drops revoked and pending ones", () => {
		expect(
			routeServers([edge, revoked, down, pending, old]).map((s) => s.name),
		).toEqual(["edge", "backup", "legacy"]);
	});

	test("is empty for no servers", () => {
		expect(routeServers([])).toEqual([]);
	});
});

function Harness({
	initial = "",
	onChange,
	invalid,
}: {
	initial?: string;
	onChange?: (id: string) => void;
	invalid?: boolean;
}) {
	const [value, setValue] = useState(initial);
	return (
		<ServerSelect
			id="srv"
			servers={[edge, old, down]}
			value={value}
			{...(invalid === undefined ? {} : { invalid })}
			onChange={(id) => {
				setValue(id);
				onChange?.(id);
			}}
		/>
	);
}

describe("ServerSelect", () => {
	test("shows the placeholder when nothing is chosen", () => {
		render(<Harness />);
		expect(screen.getByRole("combobox")).toHaveTextContent("Pick a server");
	});

	test("shows the chosen server's name", () => {
		render(<Harness initial="s-edge" />);
		expect(screen.getByRole("combobox")).toHaveTextContent("edge");
	});

	test("picking a server reports its id", async () => {
		const picked: string[] = [];
		const user = userEvent.setup();
		render(<Harness onChange={(id) => picked.push(id)} />);
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: /edge/ }));
		expect(picked).toEqual(["s-edge"]);
		expect(screen.getByRole("combobox")).toHaveTextContent("edge");
	});

	test("a v2-less online agent is listed, says why, and cannot be chosen", async () => {
		const picked: string[] = [];
		const user = userEvent.setup();
		render(<Harness onChange={(id) => picked.push(id)} />);
		await user.click(screen.getByRole("combobox"));
		const option = await screen.findByRole("option", { name: /legacy/ });
		expect(option).toHaveTextContent("agent too old: upgrade it first");
		expect(option).toHaveAttribute("aria-disabled", "true");
		await user.click(option);
		expect(picked).toEqual([]);
	});

	test("an offline server is labelled with its status", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.click(screen.getByRole("combobox"));
		const option = await screen.findByRole("option", { name: /backup/ });
		expect(option).toHaveTextContent("offline");
		expect(option).not.toHaveTextContent("too old");
	});

	test("an online current server carries no status note", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.click(screen.getByRole("combobox"));
		const option = await screen.findByRole("option", { name: /edge/ });
		expect(option).not.toHaveTextContent("online");
		expect(option).not.toHaveTextContent("too old");
	});

	test("invalid marks the trigger aria-invalid", () => {
		render(<Harness invalid />);
		expect(screen.getByRole("combobox")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	test("the trigger is not invalid by default", () => {
		render(<Harness />);
		expect(screen.getByRole("combobox")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
	});
});
