import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Server } from "#/api/models";
import { renderWithProviders } from "#/test/render";
import type { ImportFailure } from "./lib";
import { StageStep } from "./stage-step";
import { plan } from "./test-fixtures";

const server = (id: string, over: Partial<Server> = {}): Server =>
	({
		id,
		name: id,
		status: "online",
		drainState: "none",
		acceptWorkloads: true,
		buildEnabled: false,
		latest: null,
		...over,
	}) as Server;

async function stage(over: Partial<Parameters<typeof StageStep>[0]> = {}) {
	const calls = { servers: [] as string[], titles: [] as string[], again: 0 };
	const view = await renderWithProviders(
		<StageStep
			plan={plan({ name: "shop" })}
			servers={[server("alpha"), server("beta")]}
			serverId="alpha"
			onServer={(id) => calls.servers.push(id)}
			title=""
			onTitle={(t) => calls.titles.push(t)}
			includedNames={["web", "db"]}
			checking={false}
			failure={null}
			onCheckAgain={() => {
				calls.again++;
			}}
			{...over}
		/>,
	);
	return { ...view, calls };
}

describe("StageStep", () => {
	test("names the group, with the plan name as placeholder", async () => {
		const user = userEvent.setup();
		const { calls } = await stage();
		const title = screen.getByRole("textbox", { name: "Group title" });
		expect(title).toHaveAttribute("placeholder", "shop");
		expect(
			screen.getByText("The frame around the new services on the canvas."),
		).toBeInTheDocument();
		await user.type(title, "S");
		expect(calls.titles).toEqual(["S"]);
	});

	test("a stack is named, not framed", async () => {
		await stage({ stackName: "shop-stack" });
		const title = screen.getByRole("textbox", { name: "Stack name" });
		expect(title).toHaveAttribute("placeholder", "shop-stack");
		expect(
			screen.getByText(
				"The stack is one node on the canvas, with no frame around it.",
			),
		).toBeInTheDocument();
	});

	test("picks a server from the radio group", async () => {
		const user = userEvent.setup();
		const { calls } = await stage();
		expect(screen.getByRole("radio", { name: /alpha/ })).toBeChecked();
		await user.click(screen.getByRole("radio", { name: /beta/ }));
		expect(calls.servers).toEqual(["beta"]);
	});

	test("with no server it links to enrolling one", async () => {
		await stage({ servers: [], serverId: "" });
		const link = screen.getByRole("link", { name: "Add a server" });
		expect(link).toHaveAttribute("href", "/servers");
		expect(screen.queryByRole("radiogroup")).not.toBeInTheDocument();
	});

	test("lists what is staged, or says nothing is included", async () => {
		const { unmount } = await stage();
		expect(
			screen.getByText("web, db", { exact: false }).closest("p"),
		).toHaveTextContent("Stages web, db. Nothing runs until you deploy.");
		unmount();
		await stage({ includedNames: [] });
		expect(screen.getByText("No service is included.")).toBeInTheDocument();
	});

	test("shows the preflight checks for the chosen server, failures first", async () => {
		await stage({
			plan: plan({
				checks: [
					{ id: "1", message: "Disk is fine.", status: "ok" },
					{ id: "2", message: "Port 443 is taken.", status: "fail" },
				],
			}),
		});
		const preflight = screen.getByRole("region", { name: "Preflight" });
		expect(within(preflight).getByText("1 failed")).toBeInTheDocument();
		expect(
			within(preflight)
				.getAllByRole("listitem")
				.map((li) => li.textContent),
		).toEqual(["Failed: Port 443 is taken.", "Passed: Disk is fine."]);
	});

	test("shows that a check is running", async () => {
		await stage({ checking: true });
		expect(
			within(screen.getByRole("region", { name: "Preflight" })).getByText(
				"Checking",
			),
		).toBeInTheDocument();
	});

	describe("failure", () => {
		const failure: ImportFailure = {
			title: "The plan changed while staging",
			body: "Someone edited it. Nothing was staged.",
			replan: true,
		};

		test("a replannable failure offers Check again", async () => {
			const user = userEvent.setup();
			const { calls } = await stage({ failure });
			const alert = screen.getByRole("alert");
			expect(within(alert).getByText(failure.title)).toBeInTheDocument();
			expect(within(alert).getByText(failure.body)).toBeInTheDocument();
			await user.click(
				within(alert).getByRole("button", { name: "Check again" }),
			);
			expect(calls.again).toBe(1);
		});

		test("any other failure has no retry button", async () => {
			await stage({ failure: { ...failure, replan: false } });
			expect(screen.getByRole("alert")).toBeInTheDocument();
			expect(
				screen.queryByRole("button", { name: "Check again" }),
			).not.toBeInTheDocument();
		});

		test("no failure, no alert", async () => {
			await stage();
			expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		});
	});
});
