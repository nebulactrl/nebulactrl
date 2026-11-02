import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeService } from "../../lib/fixtures";
import { InspectorContext } from "../inspector-context";
import type { InspectorTab } from "../tab-ids";
import { VariablesPreview } from "./variables-preview";

function variable(key: string, value: string, isSecret = false) {
	return make("Variable", {
		key,
		value,
		isSecret,
		source: "manual",
		scope: "runtime",
	});
}

function renderPreview(opened: InspectorTab[] = []) {
	return renderWithProviders(
		<InspectorContext.Provider
			value={{ close: () => {}, openTab: (tab) => opened.push(tab) }}
		>
			<VariablesPreview service={makeService({ id: "svc-v" })} />
		</InspectorContext.Provider>,
	);
}

describe("VariablesPreview", () => {
	test("shows keys and plain values, and masks a secret without ever showing its value", async () => {
		const api = stubApi({
			"GET /services/:serviceId/variables": {
				body: make("VariableList", {
					items: [
						variable("PORT", "8080"),
						variable("API_TOKEN", "super-secret-value", true),
					],
				}),
			},
		});
		await renderPreview();
		const rows = await screen.findAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(
			within(rows[0] as HTMLElement).getByText("PORT"),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("8080"),
		).toBeInTheDocument();
		expect(within(rows[0] as HTMLElement).queryByText("secret")).toBeNull();
		expect(
			within(rows[1] as HTMLElement).getByText("API_TOKEN"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("secret"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("••••••••••••••••"),
		).toBeInTheDocument();
		expect(document.body.textContent).not.toContain("super-secret-value");
		expect(api.calls[0]?.path).toBe("/services/svc-v/variables");
		expect(api.unmatched).toEqual([]);
	});

	test("lists four and counts the rest", async () => {
		stubApi({
			"GET /services/:serviceId/variables": {
				body: make("VariableList", {
					items: ["A", "B", "C", "D", "E", "F"].map((key) =>
						variable(key, key.toLowerCase()),
					),
				}),
			},
		});
		await renderPreview();
		expect(await screen.findByText("+2 more")).toBeInTheDocument();
		expect(screen.getByText("D")).toBeInTheDocument();
		expect(screen.queryByText("E")).not.toBeInTheDocument();
	});

	test("exactly four variables show no overflow line", async () => {
		stubApi({
			"GET /services/:serviceId/variables": {
				body: make("VariableList", {
					items: ["A", "B", "C", "D"].map((k) => variable(k, "v")),
				}),
			},
		});
		await renderPreview();
		await screen.findByText("D");
		expect(screen.queryByText(/more$/)).not.toBeInTheDocument();
	});

	test("an empty service offers to add one, which opens the Variables tab", async () => {
		stubApi({
			"GET /services/:serviceId/variables": {
				body: make("VariableList", { items: [] }),
			},
		});
		const opened: InspectorTab[] = [];
		await renderPreview(opened);
		expect(await screen.findByText(/No variables yet\./)).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Add one" }));
		expect(opened).toEqual(["variables"]);
	});

	test("says so when the variables do not load", async () => {
		stubApi({
			"GET /services/:serviceId/variables": problem(500, { title: "boom" }),
		});
		await renderPreview();
		expect(
			await screen.findByText("Variables did not load."),
		).toBeInTheDocument();
	});
});
