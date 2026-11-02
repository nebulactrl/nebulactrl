import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { AclCard } from "./acl-card";
import type { MeshView } from "./model";
import { meshView } from "./test-data";

const segment = (name: string) => screen.getByRole("button", { name });

function renderCard(overrides: Partial<MeshView> = {}, isAdmin = true) {
	return renderWithProviders(
		<AclCard mesh={meshView(overrides)} isAdmin={isAdmin} />,
	);
}

describe("AclCard", () => {
	test("shows the current mode, its hint, its state and the Tailscale advisory warning", async () => {
		await renderCard({ aclMessage: "Last checked just now." });
		expect(segment("Advisory")).toHaveAttribute("aria-pressed", "true");
		expect(segment("Managed")).toHaveAttribute("aria-pressed", "false");
		expect(
			screen.getByText(
				/NebulaCtrl shows the rules it needs and checks connectivity/,
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Tailscale's default policy allows everything/),
		).toBeInTheDocument();
		expect(
			screen.getByText("Not written: add the rules yourself"),
		).toBeInTheDocument();
		expect(screen.getByText("Last checked just now.")).toBeInTheDocument();
	});

	test("an admin choosing Off sends the mode and confirms it", async () => {
		const api = stubApi({
			"PUT /mesh/settings": { body: meshView({ aclMode: "off" }) },
			"GET /mesh": { body: meshView({ aclMode: "off" }) },
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(segment("Off"));
		expect(await screen.findByText("Access policy: off")).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/mesh/settings");
		expect(put?.body).toEqual({ aclMode: "off" });
		expect(api.unmatched).toEqual([]);
	});

	test("choosing the mode that is already on sends nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderCard();
		await user.click(segment("Advisory"));
		expect(api.calls).toEqual([]);
	});

	test("a failed mode change names the problem in a toast", async () => {
		stubApi({
			"PUT /mesh/settings": problem(409, {
				code: "mesh_acl_mode_unsupported",
				detail: "unsupported",
			}),
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(segment("Off"));
		expect(
			await screen.findByText("Could not change the access policy mode"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/This tailnet cannot take the access policy from NebulaCtrl/,
			),
		).toBeInTheDocument();
	});

	test("managed on Tailscale asks first, spelling out the trade-off, and writes nothing until confirmed", async () => {
		const api = stubApi({
			"PUT /mesh/settings": { body: meshView({ aclMode: "managed" }) },
			"GET /mesh": { body: meshView({ aclMode: "managed" }) },
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(segment("Managed"));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Let NebulaCtrl write your Tailscale policy?"),
		).toBeInTheDocument();
		expect(within(dialog).getByText(/policy_file scope/)).toBeInTheDocument();
		expect(api.calls).toEqual([]);

		await user.click(
			within(dialog).getByRole("button", { name: "Write the policy" }),
		);
		expect(
			await screen.findByText("Access policy: managed"),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			aclMode: "managed",
		});
		expect(api.unmatched).toEqual([]);
	});

	test("cancelling the managed confirmation changes nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderCard();
		await user.click(segment("Managed"));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls).toEqual([]);
	});

	test("a refusal while confirming managed stays in the dialog with the mesh error's sentence", async () => {
		stubApi({
			"PUT /mesh/settings": problem(403, {
				code: "mesh_policy_scope_missing",
				detail: "scope missing",
			}),
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(segment("Managed"));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Write the policy" }),
		);
		expect(
			await within(dialog).findByText(/no policy_file scope/),
		).toBeInTheDocument();
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});

	test("managed on Headscale needs no confirmation", async () => {
		const api = stubApi({
			"PUT /mesh/settings": { body: meshView({ aclMode: "managed" }) },
			"GET /mesh": { body: meshView({ aclMode: "managed" }) },
		});
		const user = userEvent.setup();
		await renderCard({ provider: "headscale" });
		await user.click(segment("Managed"));
		expect(
			await screen.findByText("Access policy: managed"),
		).toBeInTheDocument();
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			aclMode: "managed",
		});
	});

	test("a credential that cannot write the policy disables Managed and says why", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderCard({ managedPushAvailable: false });
		expect(segment("Managed")).toBeDisabled();
		expect(
			screen.getByText(
				/Managed is not available: this tailscale credential cannot write the tailnet policy \(the OAuth client needs the policy_file scope\)\. Use advisory/,
			),
		).toBeInTheDocument();
		await user.click(segment("Managed"));
		expect(api.calls).toEqual([]);
	});

	test("the same limit on Headscale does not mention an OAuth scope", async () => {
		await renderCard({ provider: "headscale", managedPushAvailable: false });
		const text = screen.getByText(/Managed is not available/);
		expect(text.textContent).toContain("this headscale credential");
		expect(text.textContent).not.toContain("OAuth");
	});

	test("without the admin scope every mode is disabled and the card says why", async () => {
		await renderCard({}, false);
		for (const name of ["Advisory", "Managed", "Off"]) {
			expect(segment(name)).toBeDisabled();
		}
		expect(
			screen.getByText("Changing the access policy needs the admin scope."),
		).toBeInTheDocument();
	});

	test("an admin sees no scope hint", async () => {
		await renderCard();
		expect(
			screen.queryByText("Changing the access policy needs the admin scope."),
		).not.toBeInTheDocument();
	});

	test("Write rules now exists only in managed mode for an admin, and posts the apply", async () => {
		const api = stubApi({
			"POST /mesh/acl/apply": { body: meshView({ aclMode: "managed" }) },
			"GET /mesh": { body: meshView({ aclMode: "managed" }) },
		});
		const user = userEvent.setup();
		await renderCard({ aclMode: "managed", aclState: "pending" });
		await user.click(screen.getByRole("button", { name: "Write rules now" }));
		expect(await screen.findByText("Rules written")).toBeInTheDocument();
		expect(
			api.calls.some(
				(c) => c.method === "POST" && c.path === "/mesh/acl/apply",
			),
		).toBe(true);
		expect(api.unmatched).toEqual([]);
	});

	test("a failed write reports the reason", async () => {
		stubApi({
			"POST /mesh/acl/apply": problem(409, {
				code: "mesh_acl_shared_tailnet",
				detail: "shared",
			}),
		});
		const user = userEvent.setup();
		await renderCard({ aclMode: "managed" });
		await user.click(screen.getByRole("button", { name: "Write rules now" }));
		expect(
			await screen.findByText("Could not write the rules"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Another team uses this tailnet/),
		).toBeInTheDocument();
	});

	test("Write rules now is absent in advisory and for a non-admin in managed", async () => {
		await renderCard();
		expect(
			screen.queryByRole("button", { name: "Write rules now" }),
		).not.toBeInTheDocument();
	});

	test("a non-admin in managed mode cannot write rules", async () => {
		await renderCard({ aclMode: "managed" }, false);
		expect(
			screen.queryByRole("button", { name: "Write rules now" }),
		).not.toBeInTheDocument();
	});

	test("managed mode shows the managed warning; other modes hide it", async () => {
		const { unmount } = await renderCard({
			aclMode: "managed",
			aclManagedWarning:
				"Anyone with this control plane can rewrite the policy.",
		});
		expect(
			screen.getByText(
				"Anyone with this control plane can rewrite the policy.",
			),
		).toBeInTheDocument();
		unmount();
		await renderCard({
			aclMode: "advisory",
			aclManagedWarning:
				"Anyone with this control plane can rewrite the policy.",
		});
		expect(
			screen.queryByText(
				"Anyone with this control plane can rewrite the policy.",
			),
		).not.toBeInTheDocument();
	});

	test("Check now posts the check and confirms the state is current", async () => {
		const api = stubApi({
			"POST /mesh/acl/check": {
				body: make("ACLCheck", { readable: true, message: "" }),
			},
			"GET /mesh": { body: meshView() },
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(screen.getByRole("button", { name: "Check now" }));
		expect(await screen.findByText("Checked")).toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/mesh/acl/check")).toBe(true);
	});

	test("a failed check reports the reason", async () => {
		stubApi({
			"POST /mesh/acl/check": problem(502, {
				code: "mesh_coordinator_unreachable",
				detail: "down",
			}),
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(screen.getByRole("button", { name: "Check now" }));
		expect(
			await screen.findByText("Could not check the rules"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The mesh coordinator did not answer/),
		).toBeInTheDocument();
	});

	test("with the policy off there is nothing to check and no note", async () => {
		await renderCard({ aclMode: "off", aclState: "unknown" });
		expect(screen.getByRole("button", { name: "Check now" })).toBeDisabled();
		expect(screen.getByText("Off", { selector: "span" })).toBeInTheDocument();
	});

	test("the advisory snippet can be copied, and only when there is one", async () => {
		const { unmount } = await renderCard({ snippet: "// rules" });
		expect(
			screen.getByRole("button", { name: "Copy snippet" }),
		).toBeInTheDocument();
		unmount();
		await renderCard({});
		expect(
			screen.queryByRole("button", { name: "Copy snippet" }),
		).not.toBeInTheDocument();
	});

	test("Preview rules opens the preview and requests it", async () => {
		const api = stubApi({
			"POST /mesh/acl/preview": { body: { policy: "{}", rules: [] } },
		});
		const user = userEvent.setup();
		await renderCard();
		await user.click(screen.getByRole("button", { name: "Preview rules" }));
		expect(await screen.findByText("Rules the mesh needs")).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toEqual(["/mesh/acl/preview"]);
	});
});
