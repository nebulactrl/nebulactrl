import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ConnectionView } from "#/api/gen/types.gen";
import { ConnectionCard } from "./connection-card";
import { connection } from "./test-fixtures";

function setup(over: Partial<ConnectionView> = {}, testing = false) {
	const h = {
		onTest: mock(),
		onDeliveries: mock(),
		onAuthorize: mock(),
		onRefresh: mock(),
		onWebhook: mock(),
		onToken: mock(),
		onRemove: mock(),
	};
	// Spread rather than connection(over): a test passes a status the schema
	// does not list, which make() refuses and the card must still render.
	render(
		<ConnectionCard
			connection={{ ...connection(), ...over }}
			testing={testing}
			{...h}
		/>,
	);
	return h;
}

const names = () =>
	screen.getAllByRole("button").map((b) => b.textContent?.trim());

describe("ConnectionCard summary", () => {
	test("shows name, provider, host and account", () => {
		setup({ baseUrl: "https://git.example.com/sub", accountLogin: "bob" });
		expect(screen.getByRole("heading", { name: "Acme GitHub" })).toBeVisible();
		expect(
			screen.getByText("GitHub · git.example.com · as bob"),
		).toBeInTheDocument();
	});

	test("omits the account and keeps a malformed URL as typed", () => {
		setup({ accountLogin: "", baseUrl: "not a url" });
		expect(screen.getByText("GitHub · not a url")).toBeInTheDocument();
	});

	test.each([
		["active", "Connected"],
		["pending", "Waiting to finish"],
		["error", "Needs attention"],
	] as const)("status %s reads %s", (status, label) => {
		setup({ status });
		expect(screen.getByText(label)).toBeInTheDocument();
	});

	test("an unknown status is shown verbatim", () => {
		setup({ status: "suspended" as ConnectionView["status"] });
		expect(screen.getByText("suspended")).toBeInTheDocument();
	});

	test("status message is shown only when there is one", () => {
		setup({ status: "error", statusMessage: "401 from provider" });
		expect(screen.getByText("401 from provider")).toBeInTheDocument();
	});

	test.each([
		[0, /No service uses it yet\./],
		[1, /1 service deploy from it\./],
		[3, /3 services deploy from it\./],
	] as const)("service count %d reads correctly", (serviceCount, text) => {
		setup({ serviceCount });
		expect(screen.getByText(text)).toBeInTheDocument();
	});

	test("warns when the provider has no previews", () => {
		setup({ supportsPreviews: false });
		expect(
			screen.getByText(/No previews on this provider\./),
		).toBeInTheDocument();
	});
});

describe("ConnectionCard actions", () => {
	test("active GitHub App: test, deliveries, add repositories, remove", () => {
		setup();
		expect(names()).toEqual([
			"Test",
			"Deliveries",
			"Add repositories",
			"Remove",
		]);
	});

	test("pending GitHub App offers Install the App and I installed it, and no Test", async () => {
		const assign = mock();
		const original = window.location;
		Object.defineProperty(window, "location", {
			value: { ...original, assign },
			configurable: true,
		});
		try {
			const h = setup({ status: "pending" });
			expect(names()).toEqual([
				"Install the App",
				"I installed it",
				"Deliveries",
				"Remove",
			]);
			const user = userEvent.setup();
			await user.click(screen.getByRole("button", { name: "Install the App" }));
			expect(assign).toHaveBeenCalledWith(
				"https://github.com/apps/acme-app/installations/new",
			);
			await user.click(screen.getByRole("button", { name: "I installed it" }));
			expect(h.onRefresh).toHaveBeenCalledTimes(1);
		} finally {
			Object.defineProperty(window, "location", {
				value: original,
				configurable: true,
			});
		}
	});

	test("pending GitHub App without an install URL has no install button", () => {
		setup({
			status: "pending",
			github: {
				appId: 1,
				appSlug: "",
				appUrl: "",
				installUrl: "",
				installationId: 0,
			},
		});
		expect(names()).toEqual(["Deliveries", "Remove"]);
	});

	test("unfinished OAuth connection offers Authorize; Reauthorize once active", async () => {
		const h = setup({ authType: "oauth", provider: "gitlab", status: "error" });
		expect(names()).toEqual(["Authorize", "Test", "Deliveries", "Remove"]);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Authorize" }));
		expect(h.onAuthorize).toHaveBeenCalledTimes(1);
	});

	test("active OAuth connection offers Reauthorize instead", async () => {
		const h = setup({
			authType: "oauth",
			provider: "gitlab",
			status: "active",
		});
		expect(names()).toEqual(["Test", "Deliveries", "Reauthorize", "Remove"]);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Reauthorize" }));
		expect(h.onAuthorize).toHaveBeenCalledTimes(1);
	});

	test("token connection can replace its token", async () => {
		const h = setup({ authType: "token", provider: "gitea" });
		expect(names()).toEqual(["Test", "Deliveries", "Replace token", "Remove"]);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Replace token" }));
		expect(h.onToken).toHaveBeenCalledTimes(1);
	});

	test("manual-webhook connection offers webhook setup and token replacement", async () => {
		const h = setup({
			authType: "webhook_only",
			provider: "bitbucket",
			manualWebhook: true,
		});
		expect(names()).toEqual([
			"Test",
			"Deliveries",
			"Webhook setup",
			"Replace token",
			"Remove",
		]);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Webhook setup" }));
		expect(h.onWebhook).toHaveBeenCalledTimes(1);
	});

	test("Test, Deliveries and Remove call their handlers", async () => {
		const h = setup();
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "Test" }));
		await user.click(screen.getByRole("button", { name: "Deliveries" }));
		await user.click(screen.getByRole("button", { name: "Remove" }));
		expect(h.onTest).toHaveBeenCalledTimes(1);
		expect(h.onDeliveries).toHaveBeenCalledTimes(1);
		expect(h.onRemove).toHaveBeenCalledTimes(1);
	});

	test("while testing the button says so and is disabled", () => {
		setup({}, true);
		expect(screen.getByRole("button", { name: /Testing…/ })).toBeDisabled();
	});
});
