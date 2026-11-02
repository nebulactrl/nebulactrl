import { describe, expect, it } from "bun:test";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	type CredentialGuide,
	cloudflareAopGuide,
	cloudflareDns01Guide,
	headscaleApiKeyGuide,
	route53Guide,
} from "#/lib/credential-links";
import { CredentialHelp } from "./credential-help";

function fields(...values: string[]) {
	const inputs = values.map((value, i) => ({
		id: `f${i}`,
		label: `field ${i}`,
		value,
	}));
	return (
		<form>
			{inputs.map(({ id, label, value }) => (
				<input key={id} id={id} aria-label={label} defaultValue={value} />
			))}
		</form>
	);
}

const noUrl: CredentialGuide = {
	provider: "Gitea",
	cta: "Create a Gitea token",
	url: null,
	prefilled: false,
	whyNoLink: "Enter the instance URL first",
	permissions: [{ label: "read:repository" }],
	paste: "Then paste it here.",
	docs: "https://docs.gitea.com/token",
};

describe("CredentialHelp", () => {
	it("names the section after the provider", () => {
		render(<CredentialHelp guide={cloudflareDns01Guide()} fieldIds={["f0"]} />);
		expect(
			screen.getByRole("region", { name: "Cloudflare credential setup" }),
		).toBeInTheDocument();
	});

	it("opens the provider page in a new tab with a safe rel", () => {
		const guide = cloudflareDns01Guide();
		render(<CredentialHelp guide={guide} fieldIds={["f0"]} />);
		const open = screen.getByRole("button", { name: guide.cta ?? "" });
		expect(open).toHaveAttribute("href", guide.url);
		expect(open).toHaveAttribute("target", "_blank");
		expect(open).toHaveAttribute("rel", "noopener noreferrer");
	});

	it("says the permissions are selected for the reader when the link is prefilled", () => {
		render(<CredentialHelp guide={cloudflareDns01Guide()} fieldIds={[]} />);
		expect(
			screen.getByRole("heading", { name: "Selected for you" }),
		).toBeInTheDocument();
	});

	it("lists permissions to select by hand when the link is not prefilled", () => {
		render(<CredentialHelp guide={cloudflareAopGuide()} fieldIds={[]} />);
		expect(
			screen.getByRole("heading", { name: "Select these" }),
		).toBeInTheDocument();
		expect(screen.getAllByRole("listitem").length).toBeGreaterThan(0);
	});

	it("shows a copyable policy with a heading, and the heading says the policy allows it", async () => {
		const guide = route53Guide("Z0123456789ABCDEFGHIJ");
		render(<CredentialHelp guide={guide} fieldIds={[]} />);
		expect(
			screen.getByRole("heading", { name: "Allowed by the policy" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: guide.policy?.title ?? "" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /^Copy to clipboard: / }),
		).toHaveTextContent("route53:ChangeResourceRecordSets");
	});

	it("marks optional permissions and shows why each is needed", () => {
		const guide: CredentialGuide = {
			...noUrl,
			cta: null,
			permissions: [
				{ label: "read:repository", why: "clone the code" },
				{ label: "write:webhook", optional: true },
			],
		};
		render(<CredentialHelp guide={guide} fieldIds={[]} />);
		const items = screen.getAllByRole("listitem");
		expect(items).toHaveLength(2);
		expect(items[0]).toHaveTextContent("read:repository");
		expect(items[0]).toHaveTextContent("clone the code");
		expect(within(items[0] as HTMLElement).queryByText("optional")).toBeNull();
		expect(
			within(items[1] as HTMLElement).getByText("optional"),
		).toBeInTheDocument();
	});

	it("disables the button and explains when there is no link yet", () => {
		render(<CredentialHelp guide={noUrl} fieldIds={[]} />);
		expect(
			screen.getByRole("button", { name: "Create a Gitea token" }),
		).toBeDisabled();
		expect(
			screen.getByText("Enter the instance URL first"),
		).toBeInTheDocument();
	});

	it("shows no button for a provider with nothing to open", () => {
		render(<CredentialHelp guide={headscaleApiKeyGuide()} fieldIds={[]} />);
		expect(screen.queryByRole("button", { name: /Create/ })).toBeNull();
		expect(screen.getByText(/The key is shown once/)).toBeInTheDocument();
	});

	it("links to the provider's docs", () => {
		const guide = cloudflareDns01Guide();
		render(<CredentialHelp guide={guide} fieldIds={[]} />);
		const docs = screen.getByRole("button", { name: "Cloudflare docs" });
		expect(docs).toHaveAttribute("href", guide.docs);
		expect(docs).toHaveAttribute("target", "_blank");
		expect(screen.getByText(guide.paste)).toBeInTheDocument();
	});
});

describe("CredentialHelp focus on return", () => {
	function returnToTab() {
		fireEvent(document, new Event("visibilitychange"));
	}

	it("focuses the first empty field when the reader comes back after using the button", async () => {
		const guide = cloudflareDns01Guide();
		render(
			<>
				<CredentialHelp guide={guide} fieldIds={["f0", "f1", "f2"]} />
				{fields("filled", "", "")}
			</>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: guide.cta ?? "" }));
		returnToTab();
		expect(screen.getByLabelText("field 1")).toHaveFocus();
	});

	it("does not move focus when the button was never used", () => {
		render(
			<>
				<CredentialHelp guide={cloudflareDns01Guide()} fieldIds={["f0"]} />
				{fields("")}
			</>,
		);
		returnToTab();
		expect(screen.getByLabelText("field 0")).not.toHaveFocus();
	});

	it("leaves focus alone when every field is filled", async () => {
		const guide = cloudflareDns01Guide();
		render(
			<>
				<CredentialHelp guide={guide} fieldIds={["f0", "f1"]} />
				{fields("a", "b")}
			</>,
		);
		const button = screen.getByRole("button", { name: guide.cta ?? "" });
		await userEvent.setup().click(button);
		returnToTab();
		expect(screen.getByLabelText("field 0")).not.toHaveFocus();
		expect(screen.getByLabelText("field 1")).not.toHaveFocus();
	});

	it("ignores the tab going hidden", async () => {
		const guide = cloudflareDns01Guide();
		render(
			<>
				<CredentialHelp guide={guide} fieldIds={["f0"]} />
				{fields("")}
			</>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: guide.cta ?? "" }));
		const state = Object.getOwnPropertyDescriptor(document, "visibilityState");
		Object.defineProperty(document, "visibilityState", {
			value: "hidden",
			configurable: true,
		});
		try {
			returnToTab();
			expect(screen.getByLabelText("field 0")).not.toHaveFocus();
		} finally {
			if (state) Object.defineProperty(document, "visibilityState", state);
			else Reflect.deleteProperty(document, "visibilityState");
		}
	});
});
