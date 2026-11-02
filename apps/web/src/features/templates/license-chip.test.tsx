import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { LicenseChip } from "./license-chip";
import { UnrestrictedBadge } from "./unrestricted-badge";

describe("LicenseChip", () => {
	test("an OSI-approved licence is a quiet chip with the licence name", async () => {
		await renderWithProviders(
			<LicenseChip item={{ license: "MIT", licenseClass: "permissive" }} />,
		);
		expect(screen.getByText("MIT")).toBeInTheDocument();
		expect(screen.queryByText("Source-available")).not.toBeInTheDocument();
	});

	test("source-available software says so in words instead of the licence name", async () => {
		await renderWithProviders(
			<LicenseChip
				item={{ license: "BUSL-1.1", licenseClass: "source-available" }}
			/>,
		);
		expect(screen.getByText("Source-available")).toBeInTheDocument();
		expect(screen.queryByText("BUSL-1.1")).not.toBeInTheDocument();
	});

	test("proprietary software is flagged as Proprietary", async () => {
		await renderWithProviders(
			<LicenseChip
				item={{ license: "Commercial", licenseClass: "proprietary" }}
			/>,
		);
		expect(screen.getByText("Proprietary")).toBeInTheDocument();
	});

	test("focusing the chip explains the licence and links to its text", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<LicenseChip
				item={{
					license: "BUSL-1.1",
					licenseClass: "source-available",
					licenseUrl: "https://example.com/busl",
				}}
			/>,
		);
		await user.tab();
		expect(
			await screen.findByText(/not an OSI-approved licence/),
		).toBeInTheDocument();
		const link = screen.getByRole("link", {
			name: /Read the BUSL-1.1 licence/,
		});
		expect(link).toHaveAttribute("href", "https://example.com/busl");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noreferrer noopener");
	});

	test("the tooltip has no link when the template gives no licence URL", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<LicenseChip item={{ license: "MIT", licenseClass: "permissive" }} />,
		);
		await user.tab();
		expect(await screen.findByText("Licensed MIT.")).toBeInTheDocument();
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
	});
});

describe("UnrestrictedBadge", () => {
	test("names the placement requirement and explains who can change it on focus", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<UnrestrictedBadge />);
		expect(
			screen.getByText("Needs an unrestricted server"),
		).toBeInTheDocument();
		await user.tab();
		expect(
			await screen.findByText(
				/only on a server an instance admin made unrestricted/,
			),
		).toBeInTheDocument();
	});
});
