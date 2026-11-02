import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { OriginLockView } from "#/api/gen/types.gen";
import { ORIGIN_LOCK_OPT_OUT_WARNING } from "#/features/cloudflare/model";
import { renderWithProviders } from "#/test/render";
import { OriginLockSection } from "./origin-lock-section";

const lockView = (overrides: Partial<OriginLockView> = {}): OriginLockView => ({
	aop: "on",
	aopReason: "",
	aopReasonCode: "",
	enabled: true,
	ipListSource: "api",
	ipLock: true,
	optIn: true,
	permissionHint: "",
	realClientIp: true,
	reason: "",
	...overrides,
});

async function show(
	lock: OriginLockView,
	{ proxied = true, busy = false } = {},
) {
	const changes: boolean[] = [];
	await renderWithProviders(
		<OriginLockSection
			hostname="app.example.com"
			domainId="dom-1"
			lock={lock}
			proxied={proxied}
			busy={busy}
			onOptIn={(next) => changes.push(next)}
		/>,
	);
	return changes;
}

/** Selects and hold keys are slow in happy-dom, more so on a busy machine. */
const SLOW = 20_000;

describe("OriginLockSection", () => {
	test("a locked domain reads Locked with all three layers on and the switch on", async () => {
		await show(lockView());
		expect(screen.getByText("Origin locked to Cloudflare")).toBeInTheDocument();
		expect(screen.getByText("Locked")).toBeInTheDocument();
		expect(screen.getByText("IP lock")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Only Cloudflare's IP ranges can connect; everything else gets 403.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"The proxy requires the client certificate Cloudflare presents.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("Real client IP")).toBeInTheDocument();
		expect(
			screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
		).toBeChecked();
		expect(
			screen.queryByText(ORIGIN_LOCK_OPT_OUT_WARNING),
		).not.toBeInTheDocument();
	});

	test("an unlocked layer says so in words", async () => {
		await show(
			lockView({
				enabled: false,
				ipLock: false,
				aop: "off",
				realClientIp: false,
				reason: "pending",
			}),
		);
		expect(screen.getByText("Pending")).toBeInTheDocument();
		expect(
			screen.getByText("Every address can reach the server directly."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Applies while the domain is locked."),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"IP rules and analytics see the address that connected.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Waiting for the DNS record to become proxied; the lock follows.",
			),
		).toBeInTheDocument();
	});

	test("turning the lock on is immediate and sends opt-in true", async () => {
		const user = userEvent.setup();
		const changes = await show(
			lockView({ enabled: false, optIn: false, reason: "opted_out" }),
		);
		await user.click(
			screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
		);
		expect(changes).toEqual([true]);
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
	});

	test("opting out of a proxied domain asks first; keeping it locked changes nothing", async () => {
		const user = userEvent.setup();
		const changes = await show(lockView());
		await user.click(
			screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
		);
		expect(
			await screen.findByText("Open app.example.com to the internet?"),
		).toBeInTheDocument();
		expect(screen.getByText(ORIGIN_LOCK_OPT_OUT_WARNING)).toBeInTheDocument();
		expect(changes).toEqual([]);
		await user.click(screen.getByRole("button", { name: "Keep it locked" }));
		expect(changes).toEqual([]);
		expect(
			screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
		).toBeChecked();
	});

	test(
		"opting out is confirmed by holding the key, then sends opt-in false",
		async () => {
			const user = userEvent.setup();
			const changes = await show(lockView());
			await user.click(
				screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
			);
			const hold = await screen.findByRole("button", {
				name: "Hold to turn the lock off",
			});
			// A plain click is not a confirmation.
			await user.click(hold);
			expect(changes).toEqual([]);
			fireEvent.keyDown(hold, { key: "Enter" });
			await screen.findByText("Keep holding…");
			await waitFor(() => expect(changes).toEqual([false]), {
				timeout: 10_000,
			});
		},
		SLOW,
	);

	test("opting a DNS-only domain out needs no confirmation", async () => {
		const user = userEvent.setup();
		const changes = await show(lockView({ enabled: false }), {
			proxied: false,
		});
		await user.click(
			screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
		);
		expect(changes).toEqual([false]);
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
	});

	test("an opted-out proxied domain keeps the bypass warning visible", async () => {
		await show(lockView({ enabled: false, optIn: false, reason: "opted_out" }));
		expect(screen.getByText("Open")).toBeInTheDocument();
		expect(
			screen.getByText("You turned the lock off for this domain."),
		).toBeInTheDocument();
		expect(screen.getByText(ORIGIN_LOCK_OPT_OUT_WARNING)).toBeInTheDocument();
	});

	test("the switch is disabled while a change is saving", async () => {
		await show(lockView(), { busy: true });
		expect(
			screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
		).toHaveAttribute("aria-disabled", "true");
	});

	test.each([
		["tunnel", "This domain is served through a Cloudflare Tunnel."],
		["not_in_zone", "The hostname is in no connected zone."],
	] as const)(
		"the switch is disabled for a %s domain",
		async (reason, text) => {
			await show(lockView({ enabled: false, reason }));
			expect(
				screen.getByRole("switch", { name: "Lock the origin to Cloudflare" }),
			).toHaveAttribute("aria-disabled", "true");
			expect(screen.getByText(new RegExp(`^${text}`))).toBeInTheDocument();
		},
	);

	test("a refused Authenticated Origin Pulls shows why and offers the token help", async () => {
		await show(
			lockView({
				aop: "unavailable",
				aopReason: "The token lacks SSL and Certificates: Edit.",
				aopReasonCode: "permission",
			}),
		);
		expect(screen.getByText("Unavailable")).toBeInTheDocument();
		expect(
			screen.getByText("The token lacks SSL and Certificates: Edit."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Edit the Cloudflare token" }),
		).toBeInTheDocument();
	});

	test("an unavailable AOP for another reason shows no token help", async () => {
		await show(
			lockView({
				aop: "unavailable",
				aopReason: "",
				aopReasonCode: "ssl_mode",
			}),
		);
		expect(
			screen.getByText("Cloudflare could not be asked to present it."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Edit the Cloudflare token" }),
		).not.toBeInTheDocument();
	});
});
