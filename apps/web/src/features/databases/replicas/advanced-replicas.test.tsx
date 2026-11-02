import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { AdvancedReplicas } from "./advanced-replicas";
import { member, postgresService, replication } from "./replicas.fixture";

const REPLICATION = "GET /services/:id/database/replication";

const toggle = () => screen.findByRole("button", { name: /Advanced/ });

describe("AdvancedReplicas", () => {
	test("a standalone database starts collapsed, and opening reveals the panel", async () => {
		stubApi({ [REPLICATION]: { body: replication({ role: "standalone" }) } });
		const user = userEvent.setup();
		await renderWithProviders(
			<AdvancedReplicas service={postgresService()} deployed />,
		);
		const button = await toggle();
		await waitFor(() =>
			expect(button).toHaveAttribute("aria-expanded", "false"),
		);
		expect(button).toHaveTextContent("Standby replica");
		expect(button).toHaveTextContent("Show");
		expect(
			screen.queryByRole("button", { name: "Add standby replica" }),
		).not.toBeInTheDocument();

		await user.click(button);
		expect(button).toHaveAttribute("aria-expanded", "true");
		expect(button).toHaveTextContent("Hide");
		expect(
			await screen.findByRole("button", { name: "Add standby replica" }),
		).toBeEnabled();
	});

	test("a primary that already has replicas starts open", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({ role: "primary", replicas: [member()] }),
			},
		});
		await renderWithProviders(
			<AdvancedReplicas service={postgresService()} deployed />,
		);
		const button = await toggle();
		await waitFor(() =>
			expect(button).toHaveAttribute("aria-expanded", "true"),
		);
		expect(
			await screen.findByRole("list", { name: "Replicas" }),
		).toBeInTheDocument();
		expect(screen.getByText("orders-db-replica")).toBeInTheDocument();
	});

	test("a replica starts open", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({
					role: "replica",
					primary: { id: "svc-p", slug: "orders-db" },
					self: { state: "streaming", message: "" },
				}),
			},
		});
		await renderWithProviders(
			<AdvancedReplicas service={postgresService({ id: "svc-r" })} deployed />,
		);
		await waitFor(async () =>
			expect(await toggle()).toHaveAttribute("aria-expanded", "true"),
		);
		expect(await screen.findByText("replica")).toBeInTheDocument();
	});

	test("the reader's choice stands over what the state suggests", async () => {
		stubApi({
			[REPLICATION]: {
				body: replication({ role: "primary", replicas: [member()] }),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<AdvancedReplicas service={postgresService()} deployed />,
		);
		const button = await toggle();
		await waitFor(() =>
			expect(button).toHaveAttribute("aria-expanded", "true"),
		);
		await user.click(button);
		expect(button).toHaveAttribute("aria-expanded", "false");
		expect(button).toHaveTextContent("Show");
	});

	test("vanishes when the control plane has no replica API", async () => {
		const api = stubApi({
			[REPLICATION]: {
				status: 404,
				body: { status: 404, title: "x", detail: "none", code: "not_found" },
			},
		});
		const { container } = await renderWithProviders(
			<AdvancedReplicas service={postgresService()} deployed />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(container).toBeEmptyDOMElement());
		expect(screen.queryByRole("button", { name: /Advanced/ })).toBeNull();
	});
});
