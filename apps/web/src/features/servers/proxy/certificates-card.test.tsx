import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ServerCertificate } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { CertificatesCard } from "./certificates-card";

const DAY = 86_400_000;
const iso = (offsetDays: number) =>
	new Date(Date.now() + offsetDays * DAY).toISOString();

/** Valid for 101 days in total; `daysLeft` + half a day remain, so the label is stable and the validity meter (60.5 / 101) sits well away from a rounding edge. */
const cert = (
	over: Partial<ServerCertificate> & { daysLeft?: number } = {},
): ServerCertificate => {
	const { daysLeft = 60, ...rest } = over;
	return {
		id: "cert-1",
		serverId: "srv-1",
		name: "wildcard",
		status: "active",
		change: "none",
		domains: ["*.example.com", "example.com"],
		fingerprint: "aabbccddeeff00112233",
		issuer: "Acme CA",
		notBefore: iso(daysLeft + 0.5 - 101),
		notAfter: iso(daysLeft + 0.5),
		createdAt: "2026-01-01T00:00:00Z",
		appliedAt: "2026-01-02T00:00:00Z",
		daysToExpiry: daysLeft,
		statusMessage: "",
		...rest,
	};
};

const problem = (status: number, code: string, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code },
});

function card(certificates: ServerCertificate[], agentTooOld = false) {
	return (
		<CertificatesCard
			serverId="srv-1"
			serverName="edge-1"
			certificates={certificates}
			agentTooOld={agentTooOld}
		/>
	);
}

/** The list row of the certificate with this exact name. */
function rowOf(name: string): HTMLElement {
	const row = screen.getByText(name).closest("li");
	if (!row) throw new Error(`no row for ${name}`);
	return row;
}

describe("CertificatesCard", () => {
	test("says so when there are no uploaded certificates", async () => {
		await renderWithProviders(card([]));
		expect(screen.getByText("No uploaded certificates.")).toBeInTheDocument();
		expect(
			screen.queryByRole("list", { name: "Certificates" }),
		).not.toBeInTheDocument();
	});

	test("lists a certificate's name, status, names, issuer, fingerprint and remaining validity", async () => {
		await renderWithProviders(card([cert()]));
		const row = rowOf("wildcard");
		expect(within(row).getByText("wildcard")).toBeInTheDocument();
		expect(within(row).getByText("Installed")).toBeInTheDocument();
		expect(
			within(row).getByText("*.example.com, example.com"),
		).toBeInTheDocument();
		expect(
			within(row).getByText("Acme CA · SHA-256 AA:BB:CC:DD:EE:FF…"),
		).toBeInTheDocument();
		expect(within(row).getByText("60 days left")).toBeInTheDocument();
		expect(within(row).getByRole("meter", { hidden: true })).toHaveAttribute(
			"value",
			"60",
		);
		expect(within(row).queryByText("Staged")).not.toBeInTheDocument();
		expect(within(row).queryByText("Removal staged")).not.toBeInTheDocument();
	});

	test("summarises long name lists and keeps the full list in the tooltip", async () => {
		await renderWithProviders(
			card([cert({ domains: ["a.io", "b.io", "c.io", "d.io", "e.io"] })]),
		);
		const names = screen.getByText("a.io, b.io, c.io +2 more");
		expect(names).toHaveAttribute("title", "a.io, b.io, c.io, d.io, e.io");
	});

	test("a certificate without names renders an empty name line", async () => {
		await renderWithProviders(card([cert({ domains: null })]));
		const issuer = screen.getByText(/^Acme CA · SHA-256/);
		expect(issuer.previousElementSibling).toBeEmptyDOMElement();
	});

	test.each([
		["pending", "Not installed yet"],
		["error", "Problem"],
	] as const)("a %s certificate is labelled '%s'", async (status, label) => {
		await renderWithProviders(card([cert({ status })]));
		expect(screen.getByText(label)).toBeInTheDocument();
		expect(screen.queryByText("Installed")).not.toBeInTheDocument();
	});

	test("shows the status message of a certificate with a problem", async () => {
		await renderWithProviders(
			card([
				cert({ status: "error", statusMessage: "the proxy could not load it" }),
			]),
		);
		expect(screen.getByText("the proxy could not load it")).toBeInTheDocument();
	});

	test("an added certificate carries a Staged badge", async () => {
		await renderWithProviders(
			card([cert({ change: "add", status: "pending" })]),
		);
		expect(screen.getByText("Staged")).toHaveAttribute(
			"title",
			"Staged: installed on the next Deploy proxy",
		);
	});

	test("a certificate staged for removal is marked and cannot be deleted again", async () => {
		await renderWithProviders(card([cert({ change: "remove" })]));
		expect(screen.getByText("Removal staged")).toHaveAttribute(
			"title",
			"Staged: removed on the next Deploy proxy",
		);
		expect(
			screen.getByRole("button", { name: "Delete certificate wildcard" }),
		).toBeDisabled();
	});

	test("an expiring certificate reads days left, an expired one says how long ago", async () => {
		await renderWithProviders(
			card([
				cert({ id: "c1", name: "soon", daysLeft: 10 }),
				cert({
					id: "c2",
					name: "old",
					notBefore: iso(-100),
					notAfter: iso(-3 + 0.5),
				}),
			]),
		);
		expect(within(rowOf("soon")).getByText("10 days left")).toBeInTheDocument();
		expect(
			within(rowOf("old")).getByText("Expired 3 days ago"),
		).toBeInTheDocument();
	});

	test("a certificate whose validity cannot be read says so", async () => {
		await renderWithProviders(
			card([cert({ notBefore: "garbage", notAfter: "garbage" })]),
		);
		expect(screen.getByText("Unknown validity")).toBeInTheDocument();
	});

	test("Upload opens the dialog and the dialog's Cancel closes it", async () => {
		const user = userEvent.setup();
		await renderWithProviders(card([]));
		expect(screen.queryByText("Upload a certificate")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Upload" }));
		expect(await screen.findByText("Upload a certificate")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(screen.queryByText("Upload a certificate")).not.toBeInTheDocument();
	});

	test("an agent that is too old disables Upload and says why", async () => {
		await renderWithProviders(card([], true));
		const upload = screen.getByRole("button", { name: "Upload" });
		expect(upload).toBeDisabled();
		expect(upload).toHaveAttribute("title", "Upgrade the agent first");
	});

	test("Upload has no tooltip when the agent is current", async () => {
		await renderWithProviders(card([]));
		const upload = screen.getByRole("button", { name: "Upload" });
		expect(upload).toBeEnabled();
		expect(upload).not.toHaveAttribute("title");
	});

	test("deleting an installed certificate warns about pinned domains, then stages the removal", async () => {
		const api = stubApi({
			"DELETE /certificates/:certificateId": { status: 204 },
		});
		const user = userEvent.setup();
		await renderWithProviders(card([cert()]));
		await user.click(
			screen.getByRole("button", { name: "Delete certificate wildcard" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Delete wildcard?")).toBeInTheDocument();
		expect(
			within(dialog).getByText(/Domains pinned to it must be unpinned first/),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Delete certificate" }),
		);
		expect(
			await screen.findByText("Certificate removal staged"),
		).toBeInTheDocument();
		expect(
			screen.getByText("It is removed from the proxy on the next deploy."),
		).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "DELETE",
			path: "/certificates/cert-1",
		});
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test.each([
		[
			"a staged addition",
			{ change: "add" as const, appliedAt: "2026-01-02T00:00:00Z" },
		],
		["one never applied", { change: "none" as const, appliedAt: null }],
	])("deleting %s says it is deleted right away", async (_name, over) => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(card([cert(over)]));
		await user.click(
			screen.getByRole("button", { name: "Delete certificate wildcard" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText(
				"It was never installed, so it is deleted right away.",
			),
		).toBeInTheDocument();
		expect(
			within(dialog).queryByText(/Domains pinned/),
		).not.toBeInTheDocument();
	});

	test("cancelling the delete confirmation removes nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(card([cert()]));
		await user.click(
			screen.getByRole("button", { name: "Delete certificate wildcard" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		expect(api.calls).toEqual([]);
		expect(
			screen.queryByText("Certificate removal staged"),
		).not.toBeInTheDocument();
	});

	test("a certificate still in use keeps the confirmation open with the reason", async () => {
		stubApi({
			"DELETE /certificates/:certificateId": problem(
				409,
				"certificate_in_use",
				"Used by app.example.com",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(card([cert()]));
		await user.click(
			screen.getByRole("button", { name: "Delete certificate wildcard" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Delete certificate" }),
		);
		expect(
			await within(dialog).findByText("Used by app.example.com"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Certificate removal staged"),
		).not.toBeInTheDocument();
	});
});
