import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { FirewallView } from "#/api/gen/types.gen";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { FirewallSection } from "./firewall-section";

const view = (over: Partial<FirewallView> = {}): FirewallView => ({
	rules: [],
	enforcement: "none",
	hosts: ["shop.example.com"],
	deployed: true,
	server: "hetzner-01",
	geoAvailable: false,
	supported: true,
	...over,
});

const PATH = "/services/s1/firewall";

async function mount(
	initial: FirewallView,
	put?: (body: unknown) => FirewallView,
) {
	const api = stubApi({
		[`GET ${PATH}`]: { body: initial },
		[`PUT ${PATH}`]: (request: Request) =>
			request.json().then((body) => Response.json(put ? put(body) : initial)),
	});
	await renderWithProviders(
		<FirewallSection service={makeService({ id: "s1", name: "shop" })} />,
	);
	await screen.findByText(/Covers/);
	return api;
}

describe("FirewallSection", () => {
	test("without rules it offers recipes; a recipe opens as a rule that applies with one PUT", async () => {
		const user = userEvent.setup();
		const api = await mount(view(), (body) =>
			view({ rules: (body as FirewallView).rules, enforcement: "routes" }),
		);
		expect(
			screen.getByText(/every request reaches the service/),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: /Block probes for secrets/ }),
		);

		// The rule is open, reads as a sentence, and nothing is sent before Apply.
		expect(screen.getByLabelText("Name")).toHaveValue(
			"Block probes for secrets",
		);
		expect(
			screen.getByText(/Block requests when the path contains \/\.env/),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "PUT")).toHaveLength(0);

		await user.click(screen.getByRole("button", { name: "Apply firewall" }));
		expect(await screen.findByText("Firewall applied")).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.body).toMatchObject({
			rules: [
				{
					name: "Block probes for secrets",
					action: "block",
					conditions: [{ field: "path", op: "contains" }],
				},
			],
		});
	});

	test("a rule missing its values cannot be applied, and says what to fix", async () => {
		const user = userEvent.setup();
		await mount(view({ geoAvailable: false }));
		await user.click(screen.getByRole("button", { name: /Block countries/ }));
		expect(screen.getByText("Add at least one value.")).toBeInTheDocument();
		expect(screen.getByText("Needs fix")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Apply firewall" }),
		).toBeDisabled();
		// Country rules on a server without geolocation are called out before Apply.
		expect(
			screen.getByText("Country rules need geolocation"),
		).toBeInTheDocument();

		const values = screen.getByLabelText("Condition 1: values");
		fireEvent.change(values, { target: { value: "Russia" } });
		fireEvent.keyDown(values, { key: "Enter" });
		expect(screen.getByText("RU")).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByText("Add at least one value.")).toBeNull(),
		);
		expect(
			screen.getByRole("button", { name: "Apply firewall" }),
		).toBeEnabled();
	});

	test("Try a request answers with the rule that decides, before anything is applied", async () => {
		await mount(
			view({
				enforcement: "routes",
				rules: [
					{
						id: "office",
						name: "Office",
						action: "allow",
						conditions: [{ field: "ip", op: "in", values: ["203.0.113.0/24"] }],
					},
					{
						id: "admin",
						name: "Admin only",
						action: "block",
						conditions: [{ field: "path", op: "prefix", values: ["/admin"] }],
					},
				],
			}),
		);
		const verdict = () => screen.getByRole("status");
		expect(verdict()).toHaveTextContent(
			"Reaches the service. No rule matches it.",
		);
		fireEvent.change(screen.getByLabelText("Path"), {
			target: { value: "/admin/users" },
		});
		expect(verdict()).toHaveTextContent("Blocked by rule 2, Admin only.");
		fireEvent.change(screen.getByLabelText("IP address"), {
			target: { value: "203.0.113.20" },
		});
		expect(verdict()).toHaveTextContent("Allowed by rule 1, Office.");

		// Turning the allow rule off is reflected at once, unsaved.
		fireEvent.click(screen.getByRole("switch", { name: "Rule 1, Office: on" }));
		expect(verdict()).toHaveTextContent("Blocked by rule 2, Admin only.");
		expect(
			screen.getByText(/Unsaved changes, applied without a deploy/),
		).toBeInTheDocument();
	});

	test("rules move up and down, and the new order is what Apply sends", async () => {
		const user = userEvent.setup();
		const api = await mount(
			view({
				rules: [
					{
						id: "a",
						name: "First",
						action: "block",
						conditions: [{ field: "path", op: "prefix", values: ["/a"] }],
					},
					{
						id: "b",
						name: "Second",
						action: "block",
						conditions: [{ field: "path", op: "prefix", values: ["/b"] }],
					},
				],
			}),
		);
		await user.click(screen.getByRole("button", { name: "Move rule 2 up" }));
		await user.click(screen.getByRole("button", { name: "Apply firewall" }));
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "PUT")).toBe(true),
		);
		const put = api.calls.find((c) => c.method === "PUT");
		const sent = (put?.body ?? { rules: [] }) as FirewallView;
		expect(sent.rules?.map((r) => r.id)).toEqual(["b", "a"]);
	});
});
