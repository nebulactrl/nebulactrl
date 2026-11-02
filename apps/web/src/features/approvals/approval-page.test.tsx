import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ApprovalView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ApprovalPage } from "./approval-page";

function approval(over: Partial<ApprovalView> = {}): ApprovalView {
	return {
		id: "ap1",
		projectId: "p1",
		environmentId: "e1",
		status: "pending",
		canDecide: true,
		createdAt: "2026-10-31T10:00:00Z",
		expiresAt: "2026-10-31T10:10:00Z",
		requester: {
			kind: "mcp",
			tokenName: "ci-agent",
			clientName: "Claude Code",
		},
		plan: {
			op: "env.delete",
			project: "shop",
			environment: "production",
			summary: "Delete the production environment",
			effects: ["Stops every service", "Removes 2 volumes"],
			irreversible: true,
			targets: [{ id: "s1", kind: "service", name: "api" }],
		},
		...over,
	};
}

const mount = () => renderWithProviders(<ApprovalPage approvalId="ap1" />);

describe("ApprovalPage", () => {
	test("shows what will happen, who asked and what it touches", async () => {
		const api = stubApi({ "GET /approvals/ap1": { body: approval() } });
		await mount();
		expect(
			await screen.findByText("Delete the production environment"),
		).toBeInTheDocument();
		expect(screen.getByText("shop / production")).toBeInTheDocument();
		expect(screen.getByText("Stops every service")).toBeInTheDocument();
		expect(screen.getByText("Removes 2 volumes")).toBeInTheDocument();
		expect(screen.getByText("This cannot be undone")).toBeInTheDocument();
		expect(screen.getByTestId("approval-requester")).toHaveTextContent(
			"MCP token ci-agent, through Claude Code",
		);
		expect(screen.getByText("service api")).toBeInTheDocument();
		expect(screen.getByText("Waiting for approval")).toBeInTheDocument();
		expect(screen.getByTestId("approval-status")).toHaveTextContent(
			"Nothing has run",
		);
		expect(api.unmatched).toEqual([]);
	});

	test("omits the warning, effects and targets when the plan has none", async () => {
		stubApi({
			"GET /approvals/ap1": {
				body: approval({
					plan: {
						op: "x",
						project: "shop",
						environment: "production",
						summary: "Restart api",
						targets: null,
						effects: null,
					},
				}),
			},
		});
		await mount();
		await screen.findByText("Restart api");
		expect(screen.queryByText("This cannot be undone")).toBeNull();
		expect(screen.queryByText("Affects")).toBeNull();
		expect(screen.getByText("Asked by")).toBeInTheDocument();
	});

	test("approving posts the decision, shows the new status and confirms", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /approvals/ap1": { body: approval() },
			"POST /approvals/ap1/approve": {
				body: approval({
					status: "approved",
					decidedBy: "Robin",
					decidedAt: "2026-10-31T10:01:00Z",
					decidedVia: "web",
				}),
			},
		});
		await mount();
		await user.click(
			await screen.findByRole("button", { name: "Approve change" }),
		);
		expect(await screen.findByText("Decided by")).toBeInTheDocument();
		expect(screen.getByText(/Robin/)).toBeInTheDocument();
		expect(screen.getByTestId("approval-status")).toHaveTextContent(
			"The agent can repeat the call now",
		);
		// the status chip and the success toast both say Approved
		expect(
			await screen.findByText(/It runs once, exactly as shown\./),
		).toBeInTheDocument();
		expect(screen.getAllByText("Approved")).toHaveLength(2);
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"GET /approvals/ap1",
			"POST /approvals/ap1/approve",
		]);
		// decided: no more buttons offered once canDecide is false
		expect(api.unmatched).toEqual([]);
	});

	test("declining posts to the decline endpoint", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /approvals/ap1": { body: approval() },
			"POST /approvals/ap1/decline": {
				body: approval({ status: "declined", canDecide: false }),
			},
		});
		await mount();
		await user.click(await screen.findByRole("button", { name: "Decline" }));
		await waitFor(() =>
			expect(screen.getByTestId("approval-status")).toHaveTextContent(
				"The change will not run",
			),
		);
		expect(screen.queryByRole("button", { name: "Decline" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Approve change" })).toBeNull();
		expect(api.calls.at(-1)?.path).toBe("/approvals/ap1/decline");
	});

	test("a failed decision is reported and the request is reloaded", async () => {
		const user = userEvent.setup();
		let gets = 0;
		const api = stubApi({
			"GET /approvals/ap1": () => {
				gets++;
				return Response.json(
					gets === 1 ? approval() : approval({ status: "expired" }),
				);
			},
			"POST /approvals/ap1/approve": problem(409, {
				code: "approval_expired",
				detail: "request expired",
			}),
		});
		await mount();
		await user.click(
			await screen.findByRole("button", { name: "Approve change" }),
		);
		expect(
			await screen.findByText("Could not record your decision"),
		).toBeInTheDocument();
		expect(await screen.findByText("Expired")).toBeInTheDocument();
		expect(gets).toBe(2);
		expect(api.unmatched).toEqual([]);
	});

	test("a viewer who cannot decide sees why and no buttons", async () => {
		stubApi({
			"GET /approvals/ap1": { body: approval({ canDecide: false }) },
		});
		await mount();
		expect(
			await screen.findByText("You can read this request but not decide it"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Approving needs an Owner, Admin/),
		).toHaveTextContent("on shop");
		expect(screen.queryByRole("button", { name: "Approve change" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Decline" })).toBeNull();
	});

	test("a decided request that cannot be decided shows no access notice", async () => {
		stubApi({
			"GET /approvals/ap1": {
				body: approval({ status: "used", canDecide: false }),
			},
		});
		await mount();
		await screen.findByText("Approved and used");
		expect(
			screen.queryByText("You can read this request but not decide it"),
		).toBeNull();
	});

	test("an unknown request says it was not found and links home", async () => {
		stubApi({
			"GET /approvals/ap1": problem(404, {
				code: "not_found",
				detail: "not found",
			}),
		});
		await mount();
		expect(
			await screen.findByText("This approval request was not found"),
		).toBeInTheDocument();
		const link = screen.getByRole("link", { name: "Go to projects" });
		expect(link).toHaveAttribute("href", "/");
		expect(
			screen.queryByRole("button", { name: /try again|retry/i }),
		).toBeNull();
	});

	test("a server failure offers a retry that refetches", async () => {
		const user = userEvent.setup();
		let gets = 0;
		stubApi({
			"GET /approvals/ap1": () => {
				gets++;
				return gets === 1
					? Response.json({ error: "boom" }, { status: 500 })
					: Response.json(approval());
			},
		});
		await mount();
		expect(
			await screen.findByText("The approval request did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByText("Delete the production environment"),
		).toBeInTheDocument();
	});
});
