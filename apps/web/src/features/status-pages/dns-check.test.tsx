import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { DnsCheck } from "./dns-check";
import { dnsCheck } from "./sp-pages-fixtures";

const DNS = "GET /status-pages/:id/dns";

describe("DnsCheck", () => {
	it("shows a skeleton while the lookup runs, then the ready notice", async () => {
		stubApi({ [DNS]: { body: dnsCheck() } });
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		expect(screen.getByRole("status", { name: "Checking DNS" })).toBeVisible();
		expect(await screen.findByText("DNS is ready")).toBeInTheDocument();
		expect(
			screen.getByText("The hostname points at this control plane."),
		).toBeInTheDocument();
		expect(screen.queryByRole("status", { name: "Checking DNS" })).toBeNull();
		expect(
			screen.getByText("203.0.113.10", { selector: "span" }),
		).toBeVisible();
	});

	it("asks for the cached lookup of the page first", async () => {
		const api = stubApi({ [DNS]: { body: dnsCheck() } });
		await renderWithProviders(<DnsCheck pageId="page-7" />);
		await screen.findByText("DNS is ready");
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/status-pages/page-7/dns");
		expect(api.calls[0]?.query.get("fresh")).toBe("false");
	});

	it("says the hostname resolves through Cloudflare when it is proxied", async () => {
		stubApi({ [DNS]: { body: dnsCheck({ proxied: true }) } });
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		expect(
			await screen.findByText("Resolves through Cloudflare"),
		).toBeInTheDocument();
	});

	it("lists the records to create when the hostname points elsewhere", async () => {
		stubApi({
			[DNS]: {
				body: dnsCheck({
					status: "mismatch",
					resolved: ["198.51.100.4"],
					message: "Resolved to a different address.",
					records: [
						{ name: "status.acme.test", type: "A", value: "203.0.113.10" },
						{ name: "status.acme.test", type: "AAAA", value: "2001:db8::1" },
					],
				}),
			},
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		expect(await screen.findByText("DNS points elsewhere")).toBeInTheDocument();
		expect(
			screen.getByText(/Change its records to the ones below/),
		).toBeInTheDocument();
		// The server's own message adds to the summary for a mismatch.
		expect(
			screen.getByText("Resolved to a different address."),
		).toBeInTheDocument();
		expect(screen.getByText("Create these records")).toBeInTheDocument();
		expect(
			screen.getByRole("button", {
				name: "Copy the A record 203.0.113.10",
			}),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Copy the AAAA record 2001:db8::1" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Copy all" }),
		).toBeInTheDocument();
		expect(screen.getByText("198.51.100.4")).toBeInTheDocument();
	});

	it("tells the user to create records when none exist yet", async () => {
		stubApi({
			[DNS]: { body: dnsCheck({ status: "missing", resolved: [] }) },
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		expect(
			await screen.findByText("DNS has no records yet"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Resolves to nothing right now."),
		).toBeInTheDocument();
	});

	it("explains that the control plane's address is unknown when no records come back", async () => {
		stubApi({
			[DNS]: {
				body: dnsCheck({ status: "missing", records: null, resolved: null }),
			},
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		expect(
			await screen.findByText(/does not know its public address yet/),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Copy all" })).toBeNull();
	});

	it("copies every record as a line with the Copy all button", async () => {
		const user = userEvent.setup();
		const written: string[] = [];
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: {
				writeText: async (text: string) => {
					written.push(text);
				},
			},
		});
		stubApi({
			[DNS]: {
				body: dnsCheck({
					status: "missing",
					records: [
						{ name: "status.acme.test", type: "A", value: "203.0.113.10" },
						{ name: "status.acme.test", type: "AAAA", value: "2001:db8::1" },
					],
				}),
			},
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		await user.click(await screen.findByRole("button", { name: "Copy all" }));
		await waitFor(() => expect(written).toHaveLength(1));
		expect(written[0]).toBe(
			"status.acme.test  A  203.0.113.10\nstatus.acme.test  AAAA  2001:db8::1",
		);
	});

	it("shows what the check could not tell, with the server's message", async () => {
		stubApi({
			[DNS]: {
				body: dnsCheck({ status: "unknown", message: "Resolver timed out." }),
			},
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		expect(
			await screen.findByText("DNS could not be checked"),
		).toBeInTheDocument();
		expect(screen.getByText("Resolver timed out.")).toBeInTheDocument();
	});

	it("says Cloudflare has the record while public DNS catches up", async () => {
		stubApi({
			[DNS]: {
				body: dnsCheck({
					status: "missing",
					cloudflareRecord: true,
					resolved: [],
				}),
			},
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		expect(
			await screen.findByText("Cloudflare has the record"),
		).toBeInTheDocument();
		expect(screen.getByText(/up to 30 minutes/)).toBeInTheDocument();
	});

	it("adds the new-name heads-up while the name is under 30 minutes old", async () => {
		stubApi({
			[DNS]: {
				body: dnsCheck({ nameChangedAt: new Date().toISOString() }),
			},
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		expect(await screen.findByText("DNS is ready")).toBeInTheDocument();
		expect(
			screen.getByText(/New names can take up to 30 minutes/),
		).toBeVisible();
	});

	it("leaves out the new-name heads-up for an old name", async () => {
		stubApi({
			[DNS]: {
				body: dnsCheck({
					nameChangedAt: new Date(Date.now() - 3 * 3_600_000).toISOString(),
				}),
			},
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		await screen.findByText("DNS is ready");
		expect(screen.queryByText(/New names can take up to/)).toBeNull();
	});

	it("lists no records to create inside the Cloudflare panel and reads uncached", async () => {
		const api = stubApi({
			[DNS]: { body: dnsCheck({ status: "missing" }) },
		});
		await renderWithProviders(<DnsCheck pageId="page-1" managed />);
		expect(
			await screen.findByText("DNS has no records yet"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The Cloudflare section above creates it/),
		).toBeInTheDocument();
		expect(screen.queryByText("Create these records")).toBeNull();
		expect(api.calls[0]?.query.get("fresh")).toBe("true");
	});

	it("rechecks past the cache on the first Recheck and again on the next", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [DNS]: { body: dnsCheck() } });
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		await screen.findByText("DNS is ready");

		await user.click(screen.getByRole("button", { name: "Recheck" }));
		await waitFor(() => expect(api.calls).toHaveLength(2));
		expect(api.calls[1]?.query.get("fresh")).toBe("true");
		await screen.findByRole("button", { name: "Recheck" });

		await user.click(screen.getByRole("button", { name: "Recheck" }));
		await waitFor(() => expect(api.calls).toHaveLength(3));
		expect(api.calls[2]?.query.get("fresh")).toBe("true");
	});

	it("shows a fresh answer after Recheck", async () => {
		const user = userEvent.setup();
		const answers = [
			dnsCheck({ status: "missing", resolved: [] }),
			dnsCheck({ status: "ok" }),
		];
		stubApi({
			[DNS]: () => Response.json(answers.shift() ?? dnsCheck()),
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		await screen.findByText("DNS has no records yet");
		await user.click(screen.getByRole("button", { name: "Recheck" }));
		expect(await screen.findByText("DNS is ready")).toBeInTheDocument();
		expect(screen.queryByText("DNS has no records yet")).toBeNull();
	});

	it("asks for a hostname first when the page has none", async () => {
		stubApi({
			[DNS]: {
				status: 422,
				body: { status: 422, code: "no_hostname", detail: "no hostname" },
			},
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		expect(
			await screen.findByText("Save a hostname first."),
		).toBeInTheDocument();
		expect(screen.queryByText("no hostname")).toBeNull();
	});

	it("shows the server's message for another failure and retries on request", async () => {
		const user = userEvent.setup();
		let fail = true;
		const api = stubApi({
			[DNS]: () =>
				fail
					? Response.json(
							{
								status: 502,
								code: "dns_unavailable",
								detail: "Resolver down.",
							},
							{ status: 502 },
						)
					: Response.json(dnsCheck()),
		});
		await renderWithProviders(<DnsCheck pageId="page-1" />);
		const alert = await screen.findByRole("alert");
		expect(within(alert).getByText("DNS could not be checked")).toBeVisible();
		expect(within(alert).getByText(/Resolver down\./)).toBeVisible();

		fail = false;
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByText("DNS is ready")).toBeInTheDocument();
		expect(api.calls).toHaveLength(2);
	});
});
