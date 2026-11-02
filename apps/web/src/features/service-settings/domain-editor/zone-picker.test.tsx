import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { ZonePicker } from "./zone-picker";

const connection = (configured: boolean) => ({
	body: {
		configured,
		accountId: "",
		accountName: "",
		accounts: [],
		status: configured ? "ok" : "",
		statusMessage: "",
		tokenExpiresAt: null,
		verifiedAt: null,
		warnings: [],
		zoneCount: 2,
		zonesSyncedAt: null,
	},
});

const zone = (name: string) => ({
	id: `zone-${name}`,
	name,
	accountId: "acc",
	accountName: "Acme",
	paused: false,
	status: "active",
});

const zones = {
	body: { items: [zone("example.com"), zone("shop.example.com")] },
};

/** Keeps the typed hostname, like the add-domain form does. */
function Harness({
	initial = "",
	disabled = false,
	onPicked,
}: {
	initial?: string;
	disabled?: boolean;
	onPicked?(hostname: string): void;
}) {
	const [hostname, setHostname] = useState(initial);
	return (
		<>
			<output aria-label="hostname">{hostname}</output>
			<ZonePicker
				hostname={hostname}
				disabled={disabled}
				onPick={(next) => {
					onPicked?.(next);
					setHostname(next);
				}}
			/>
		</>
	);
}

/** Selects and hold keys are slow in happy-dom, more so on a busy machine. */
const SLOW = 20_000;

describe("ZonePicker", () => {
	test(
		"renders nothing, and reads no zones, without a Cloudflare connection",
		async () => {
			const api = stubApi({
				"GET /integrations/cloudflare": connection(false),
				"GET /integrations/cloudflare/zones": zones,
			});
			await renderWithProviders(<Harness initial="app.example.com" />);
			await waitFor(() => expect(api.calls).toHaveLength(1));
			expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
			expect(screen.queryByText("Zone")).not.toBeInTheDocument();
			expect(api.calls.map((c) => c.path)).toEqual([
				"/integrations/cloudflare",
			]);
		},
		SLOW,
	);

	test(
		"renders nothing when the account has no zones",
		async () => {
			const api = stubApi({
				"GET /integrations/cloudflare": connection(true),
				"GET /integrations/cloudflare/zones": { body: { items: null } },
			});
			await renderWithProviders(<Harness />);
			await waitFor(() => expect(api.calls).toHaveLength(2));
			expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
		},
		SLOW,
	);

	test(
		"invites picking a zone while no hostname is typed",
		async () => {
			stubApi({
				"GET /integrations/cloudflare": connection(true),
				"GET /integrations/cloudflare/zones": zones,
			});
			await renderWithProviders(<Harness />);
			expect(
				await screen.findByText(
					"Pick a zone to build the hostname, or type one.",
				),
			).toBeInTheDocument();
			const trigger = screen.getByRole("combobox", { name: "Cloudflare zone" });
			expect(trigger).toHaveTextContent("Pick a zone…");
		},
		SLOW,
	);

	test(
		"says a typed hostname in a zone gets its record, starting proxied",
		async () => {
			stubApi({
				"GET /integrations/cloudflare": connection(true),
				"GET /integrations/cloudflare/zones": zones,
			});
			await renderWithProviders(<Harness initial="app.example.com" />);
			expect(
				await screen.findByText(
					"In example.com: NebulaCtrl creates its DNS record and keeps it in step. It starts proxied.",
				),
			).toBeInTheDocument();
			expect(
				screen.getByRole("combobox", { name: "Cloudflare zone" }),
			).toHaveTextContent("example.com");
		},
		SLOW,
	);

	test("prefers the longest matching zone and starts a wildcard DNS only", async () => {
		stubApi({
			"GET /integrations/cloudflare": connection(true),
			"GET /integrations/cloudflare/zones": zones,
		});
		await renderWithProviders(<Harness initial="*.shop.example.com" />);
		expect(
			await screen.findByText(
				"In shop.example.com: NebulaCtrl creates its DNS record and keeps it in step. It starts DNS only.",
			),
		).toBeInTheDocument();
	});

	test(
		"tells the user to point DNS themselves when no zone matches",
		async () => {
			stubApi({
				"GET /integrations/cloudflare": connection(true),
				"GET /integrations/cloudflare/zones": zones,
			});
			await renderWithProviders(<Harness initial="app.other.org" />);
			expect(
				await screen.findByText(
					"In none of your Cloudflare zones: point DNS yourself.",
				),
			).toBeInTheDocument();
			expect(
				screen.getByRole("combobox", { name: "Cloudflare zone" }),
			).toHaveTextContent("No matching zone");
		},
		SLOW,
	);

	test(
		"picking a zone rebuilds the hostname under it, keeping the first label",
		async () => {
			stubApi({
				"GET /integrations/cloudflare": connection(true),
				"GET /integrations/cloudflare/zones": zones,
			});
			const picked: string[] = [];
			const user = userEvent.setup();
			await renderWithProviders(
				<Harness initial="shop.other.org" onPicked={(h) => picked.push(h)} />,
			);
			await user.click(
				await screen.findByRole("combobox", { name: "Cloudflare zone" }),
			);
			await user.click(screen.getByRole("option", { name: "example.com" }));
			expect(picked).toEqual(["shop.example.com"]);
			expect(screen.getByLabelText("hostname")).toHaveTextContent(
				"shop.example.com",
			);
			// The typed hostname now falls into the zone.
			expect(
				await screen.findByText(/^In shop\.example\.com: NebulaCtrl creates/),
			).toBeInTheDocument();
		},
		SLOW,
	);

	test(
		"picking a zone with nothing typed builds app.<zone>",
		async () => {
			stubApi({
				"GET /integrations/cloudflare": connection(true),
				"GET /integrations/cloudflare/zones": zones,
			});
			const user = userEvent.setup();
			await renderWithProviders(<Harness />);
			await user.click(
				await screen.findByRole("combobox", { name: "Cloudflare zone" }),
			);
			await user.click(screen.getByRole("option", { name: "example.com" }));
			expect(screen.getByLabelText("hostname")).toHaveTextContent(
				"app.example.com",
			);
		},
		SLOW,
	);

	test(
		"is disabled with the form",
		async () => {
			stubApi({
				"GET /integrations/cloudflare": connection(true),
				"GET /integrations/cloudflare/zones": zones,
			});
			await renderWithProviders(<Harness disabled />);
			expect(
				await screen.findByRole("combobox", { name: "Cloudflare zone" }),
			).toBeDisabled();
		},
		SLOW,
	);
});
