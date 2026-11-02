import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { SettingsNotice, SettingsPlate, SettingsSection } from "./section";

describe("SettingsSection", () => {
	test("is a region named by its h2, with the description under it", async () => {
		await renderWithProviders(
			<SettingsSection
				id="team-members"
				title="Members"
				description="People in this team."
			>
				<p>body</p>
			</SettingsSection>,
		);
		const region = screen.getByRole("region", { name: "Members" });
		expect(region).toHaveAttribute("id", "team-members");
		expect(
			within(region).getByRole("heading", { level: 2, name: "Members" }),
		).toBeInTheDocument();
		expect(
			within(region).getByText("People in this team."),
		).toBeInTheDocument();
		expect(within(region).getByText("body")).toBeInTheDocument();
	});

	test("without docs there is no Learn more link", async () => {
		await renderWithProviders(
			<SettingsSection id="s" title="T" description="D">
				<p>body</p>
			</SettingsSection>,
		);
		expect(screen.queryByRole("link")).toBeNull();
	});

	test("with docs the description ends in a link to that docs page", async () => {
		await renderWithProviders(
			<SettingsSection id="s" title="T" description="D" docs="teams#roles">
				<p>body</p>
			</SettingsSection>,
		);
		const link = screen.getByRole("link");
		expect(link).toHaveAttribute("href", expect.stringContaining("teams"));
		expect(link.getAttribute("href")).toContain("#roles");
	});
});

describe("SettingsPlate", () => {
	test("is a group named by its h3, holding its content and actions", async () => {
		await renderWithProviders(
			<SettingsPlate
				id="profile-account"
				title="Account"
				actions={<button type="button">Act</button>}
			>
				<p>inside</p>
			</SettingsPlate>,
		);
		const group = screen.getByRole("group", { name: "Account" });
		expect(group).toHaveAttribute("id", "profile-account");
		expect(
			within(group).getByRole("heading", { level: 3, name: "Account" }),
		).toHaveAttribute("id", "profile-account-heading");
		expect(within(group).getByText("inside")).toBeInTheDocument();
		expect(
			within(group).getByRole("button", { name: "Act" }),
		).toBeInTheDocument();
	});

	test("without an id the heading carries no id and the content still renders", async () => {
		await renderWithProviders(
			<SettingsPlate title="Loose">
				<p>inside</p>
			</SettingsPlate>,
		);
		expect(
			screen.getByRole("heading", { level: 3, name: "Loose" }),
		).not.toHaveAttribute("id");
		expect(screen.getByText("inside")).toBeInTheDocument();
	});

	test("a tip adds an info button whose label defaults to About <title>", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<SettingsPlate title="Two-step verification" tip="Why this matters.">
				<p>inside</p>
			</SettingsPlate>,
		);
		const tip = screen.getByRole("button", {
			name: "About two-step verification",
		});
		await user.hover(tip);
		expect(await screen.findByText("Why this matters.")).toBeInTheDocument();
	});

	test("tipLabel overrides the default label", async () => {
		await renderWithProviders(
			<SettingsPlate title="Keys" tip="Details." tipLabel="What keys are">
				<p>inside</p>
			</SettingsPlate>,
		);
		expect(
			screen.getByRole("button", { name: "What keys are" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "About keys" })).toBeNull();
	});

	test("no tip, no info button", async () => {
		await renderWithProviders(
			<SettingsPlate title="Plain">
				<p>inside</p>
			</SettingsPlate>,
		);
		expect(screen.queryByRole("button")).toBeNull();
	});
});

describe("SettingsNotice", () => {
	test("is a status message with the sentence", async () => {
		await renderWithProviders(
			<SettingsNotice>Owners and admins manage this.</SettingsNotice>,
		);
		expect(screen.getByRole("status")).toHaveTextContent(
			"Owners and admins manage this.",
		);
	});
});
