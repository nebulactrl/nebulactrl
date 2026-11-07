import { describe, expect, it, mock } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type {
	StatusPageComponentView,
	StatusPageView,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { formFromPage, type PageForm } from "./model";
import {
	GeneralSection,
	HostnameSection,
	PublishSection,
} from "./page-sections";
import { dnsCheck } from "./sp-pages-fixtures";

function page(over: Partial<StatusPageView> = {}): StatusPageView {
	return {
		id: "page-1",
		title: "Acme status",
		slug: "acme-status",
		description: "How we are doing",
		footer: "ops@acme.test",
		showUptime: true,
		hostname: "status.acme.test",
		published: false,
		components: [],
		createdAt: "2026-10-01T00:00:00Z",
		updatedAt: "2026-10-01T00:00:00Z",
		...over,
	};
}

const COMPONENT: StatusPageComponentView = {
	serviceId: "web",
	serviceName: "web",
	displayName: "Website",
	groupName: "",
	url: "",
	projectName: "Acme",
	environmentName: "production",
};

function GeneralHarness({
	initial,
	errors = {},
	readOnly,
	seen,
}: {
	initial: PageForm;
	errors?: Parameters<typeof GeneralSection>[0]["errors"];
	readOnly?: boolean;
	seen?: (change: Partial<PageForm>) => void;
}) {
	const [form, setForm] = useState(initial);
	return (
		<GeneralSection
			form={form}
			errors={errors}
			{...(readOnly === undefined ? {} : { readOnly })}
			onChange={(change) => {
				seen?.(change);
				setForm({ ...form, ...change });
			}}
		/>
	);
}

describe("GeneralSection", () => {
	it("shows what the page says about itself", async () => {
		await renderWithProviders(
			<GeneralHarness initial={formFromPage(page())} />,
		);
		expect(screen.getByLabelText("Title")).toHaveValue("Acme status");
		expect(screen.getByLabelText("Short name")).toHaveValue("acme-status");
		expect(screen.getByLabelText("Description")).toHaveValue(
			"How we are doing",
		);
		expect(screen.getByLabelText("Footer")).toHaveValue("ops@acme.test");
		expect(screen.getByRole("switch", { name: "Show uptime" })).toBeChecked();
		expect(screen.getByText("Percentages shown")).toBeInTheDocument();
	});

	it.each([
		["Title", "title", "New title"],
		["Short name", "slug", "new-name"],
		["Description", "description", "A sentence."],
		["Footer", "footer", "Call us."],
	] as const)(
		"reports a change to %s as it is typed",
		async (label, field, text) => {
			const user = userEvent.setup();
			const seen: Partial<PageForm>[] = [];
			await renderWithProviders(
				<GeneralHarness
					initial={{ ...formFromPage(page()), [field]: "" }}
					seen={(change) => seen.push(change)}
				/>,
			);
			await user.type(screen.getByLabelText(label), text);
			expect(seen.at(-1)).toEqual({ [field]: text });
			expect(screen.getByLabelText(label)).toHaveValue(text);
		},
	);

	it("shows each field's problem and marks the field invalid", async () => {
		await renderWithProviders(
			<GeneralHarness
				initial={formFromPage(page())}
				errors={{
					title: "Enter a title.",
					slug: "Enter a short name.",
					description: "A description is at most 500 characters.",
					footer: "A footer is at most 500 characters.",
				}}
			/>,
		);
		for (const [label, message] of [
			["Title", "Enter a title."],
			["Short name", "Enter a short name."],
			["Description", "A description is at most 500 characters."],
			["Footer", "A footer is at most 500 characters."],
		] as const) {
			expect(screen.getByLabelText(label)).toBeInvalid();
			expect(screen.getByText(message)).toBeInTheDocument();
		}
	});

	it("marks fields valid when there are no problems", async () => {
		await renderWithProviders(
			<GeneralHarness initial={formFromPage(page())} />,
		);
		expect(screen.getByLabelText("Title")).toBeValid();
		expect(screen.getByLabelText("Description")).toBeValid();
		expect(screen.getByLabelText("Footer")).toBeValid();
	});

	it("toggles uptime percentages and says what that shows", async () => {
		const user = userEvent.setup();
		const seen: Partial<PageForm>[] = [];
		await renderWithProviders(
			<GeneralHarness
				initial={formFromPage(page({ showUptime: true }))}
				seen={(change) => seen.push(change)}
			/>,
		);
		await user.click(screen.getByRole("switch", { name: "Show uptime" }));
		expect(seen).toEqual([{ showUptime: false }]);
		expect(screen.getByText("Bars only")).toBeInTheDocument();
		expect(screen.queryByText("Percentages shown")).not.toBeInTheDocument();
	});

	it("disables the uptime switch when read-only", async () => {
		await renderWithProviders(
			<GeneralHarness initial={formFromPage(page())} readOnly />,
		);
		expect(screen.getByRole("switch", { name: "Show uptime" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
	});

	it("leaves the uptime switch usable by default", async () => {
		await renderWithProviders(
			<GeneralHarness initial={formFromPage(page())} />,
		);
		expect(
			screen.getByRole("switch", { name: "Show uptime" }),
		).not.toHaveAttribute("aria-disabled", "true");
	});
});

function stubDns() {
	return stubApi({
		"GET /status-pages/:id/cloudflare": problem(404, {
			code: "not_found",
			title: "Not found",
		}),
		"GET /status-pages/:id/dns": { body: dnsCheck() },
	});
}

describe("HostnameSection", () => {
	function section(saved: StatusPageView, hostname: string, error?: string) {
		const onChange = mock((_change: Partial<PageForm>) => {});
		const ui = (
			<HostnameSection
				page={saved}
				form={{ ...formFromPage(saved), hostname }}
				error={error}
				onChange={onChange}
			/>
		);
		return { ui, onChange };
	}

	it("asks for a hostname when none is saved or typed", async () => {
		const api = stubDns();
		const { ui } = section(page({ hostname: null }), "");
		await renderWithProviders(ui);
		expect(
			screen.getByText(
				"Add a hostname and save to see the DNS records it needs.",
			),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	it("asks to save a hostname that is typed but not saved yet", async () => {
		const api = stubDns();
		const { ui } = section(page({ hostname: null }), "status.new.test");
		await renderWithProviders(ui);
		expect(
			screen.getByText("Save the hostname to see the DNS records it needs."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	it("says the DNS check still applies to the saved name while the hostname is edited", async () => {
		const api = stubDns();
		const { ui } = section(page(), "status.other.test");
		await renderWithProviders(ui);
		const note = screen.getByText(/Save the new hostname to check its DNS/);
		expect(note).toHaveTextContent("applies to status.acme.test until then");
		expect(api.calls).toEqual([]);
	});

	it("checks the DNS of the saved hostname when it is unchanged", async () => {
		const api = stubDns();
		const { ui } = section(page(), "status.acme.test");
		await renderWithProviders(ui);
		expect(await screen.findByText(/Resolves to now/)).toHaveTextContent(
			"203.0.113.10",
		);
		expect(
			screen.getByRole("button", { name: "Copy the A record 203.0.113.10" }),
		).toBeInTheDocument();
		expect(screen.queryByText(/Save the new hostname/)).not.toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toEqual([
			"/status-pages/page-1/cloudflare",
			"/status-pages/page-1/dns",
		]);
		expect(api.unmatched).toEqual([]);
	});

	it("treats a differently written form of the saved hostname as unchanged", async () => {
		stubDns();
		const { ui } = section(page(), "  STATUS.Acme.test.  ");
		await renderWithProviders(ui);
		expect(await screen.findByText(/Resolves to now/)).toBeInTheDocument();
		expect(screen.queryByText(/Save the new hostname/)).not.toBeInTheDocument();
		expect(
			screen.queryByText(/Save the hostname to see/),
		).not.toBeInTheDocument();
	});

	it("reports the hostname as it is typed", async () => {
		const user = userEvent.setup();
		stubDns();
		const { ui, onChange } = section(page({ hostname: null }), "");
		await renderWithProviders(ui);
		await user.type(screen.getByLabelText("Public hostname"), "s");
		expect(onChange).toHaveBeenCalledWith({ hostname: "s" });
	});

	it("shows the hostname's problem", async () => {
		stubDns();
		const { ui } = section(
			page({ hostname: null }),
			"nope",
			"Enter a valid hostname, like status.example.com.",
		);
		await renderWithProviders(ui);
		expect(screen.getByLabelText("Public hostname")).toBeInvalid();
		expect(
			screen.getByText("Enter a valid hostname, like status.example.com."),
		).toBeInTheDocument();
	});
});

describe("PublishSection", () => {
	function section(
		over: Partial<StatusPageView> = {},
		props: Partial<Parameters<typeof PublishSection>[0]> = {},
	) {
		// Only the checked value: the switch also passes event details, a DOM
		// graph that a failing matcher would take minutes to print.
		const changes: boolean[] = [];
		const onChange = (published: boolean) => {
			changes.push(published);
		};
		const ui = (
			<PublishSection
				page={page(over)}
				dirty={false}
				pending={false}
				problem={undefined}
				onChange={onChange}
				{...props}
			/>
		);
		return { ui, changes };
	}

	it("lists what publishing needs, with what is met", async () => {
		const none = section({ hostname: null, components: [] });
		const { unmount } = await renderWithProviders(none.ui);
		let needs = screen.getByRole("list", { name: "What publishing needs" });
		expect(within(needs).getByText("A hostname")).toHaveTextContent(
			"A hostname, still needed",
		);
		expect(within(needs).getByText("At least one component")).toHaveTextContent(
			"At least one component, still needed",
		);
		unmount();

		const met = section({ components: [COMPONENT] });
		await renderWithProviders(met.ui);
		needs = screen.getByRole("list", { name: "What publishing needs" });
		expect(within(needs).getByText("A hostname")).toHaveTextContent(
			"A hostname, done",
		);
		expect(within(needs).getByText("At least one component")).toHaveTextContent(
			"At least one component, done",
		);
	});

	it("treats a null component list as no components", async () => {
		const { ui } = section({ components: null });
		await renderWithProviders(ui);
		expect(screen.getByText("At least one component")).toHaveTextContent(
			"still needed",
		);
	});

	it("treats an empty hostname as missing", async () => {
		const { ui } = section({ hostname: "" });
		await renderWithProviders(ui);
		expect(screen.getByText("A hostname")).toHaveTextContent("still needed");
	});

	it("shows a draft as not visible, with no link to open", async () => {
		const { ui } = section({ published: false });
		await renderWithProviders(ui);
		expect(
			screen.getByText("Draft, not visible to anyone"),
		).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Published" })).not.toBeChecked();
		expect(
			screen.queryByRole("button", { name: /Open page/ }),
		).not.toBeInTheDocument();
	});

	it("shows a published page as live with a link to its hostname", async () => {
		const { ui } = section({ published: true, components: [COMPONENT] });
		await renderWithProviders(ui);
		expect(screen.getByText("Live on its hostname")).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Published" })).toBeChecked();
		const open = screen.getByRole("button", { name: /Open page/ });
		expect(open).toHaveAttribute("href", "https://status.acme.test/");
		expect(open).toHaveAttribute("target", "_blank");
	});

	it("offers no link for a published page that has lost its hostname", async () => {
		const { ui } = section({ published: true, hostname: null });
		await renderWithProviders(ui);
		expect(
			screen.queryByRole("button", { name: /Open page/ }),
		).not.toBeInTheDocument();
	});

	it("asks to publish when the switch is turned on", async () => {
		const user = userEvent.setup();
		const { ui, changes } = section({
			published: false,
			components: [COMPONENT],
		});
		await renderWithProviders(ui);
		await user.click(screen.getByRole("switch", { name: "Published" }));
		expect(changes).toEqual([true]);
	});

	it("asks to unpublish when the switch is turned off", async () => {
		const user = userEvent.setup();
		const { ui, changes } = section({ published: true });
		await renderWithProviders(ui);
		await user.click(screen.getByRole("switch", { name: "Published" }));
		expect(changes).toEqual([false]);
	});

	it("blocks publishing a draft while there are unsaved edits, and says why", async () => {
		const user = userEvent.setup();
		const { ui, changes } = section({ published: false }, { dirty: true });
		await renderWithProviders(ui);
		const toggle = screen.getByRole("switch", { name: "Published" });
		expect(toggle).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(
				"Save or discard your changes first: publishing uses what is saved.",
			),
		).toBeInTheDocument();
		await user.click(toggle);
		expect(changes).toEqual([]);
	});

	it("still lets a published page be unpublished with unsaved edits", async () => {
		const { ui } = section({ published: true }, { dirty: true });
		await renderWithProviders(ui);
		expect(
			screen.getByRole("switch", { name: "Published" }),
		).not.toHaveAttribute("aria-disabled", "true");
		expect(
			screen.queryByText(/Save or discard your changes first/),
		).not.toBeInTheDocument();
	});

	it("shows progress and locks the switch while a change is in flight", async () => {
		const first = section({ published: false }, { pending: true });
		const { unmount } = await renderWithProviders(first.ui);
		expect(screen.getByText("Publishing…")).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Published" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		unmount();

		const second = section({ published: true }, { pending: true });
		await renderWithProviders(second.ui);
		expect(screen.getByText("Taking the page down…")).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Published" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
	});

	it("shows the control plane's refusal under the switch", async () => {
		const { ui } = section(
			{ published: false },
			{ problem: "Add a hostname before publishing the page." },
		);
		await renderWithProviders(ui);
		expect(
			screen.getByText("Add a hostname before publishing the page."),
		).toBeInTheDocument();
	});

	it("disables the switch for a read-only viewer", async () => {
		const { ui } = section({ published: true }, { readOnly: true });
		await renderWithProviders(ui);
		expect(screen.getByRole("switch", { name: "Published" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
	});
});
