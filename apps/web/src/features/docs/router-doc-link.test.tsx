import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import { RouterDocLink } from "./router-doc-link";

describe("RouterDocLink", () => {
	test("links to the docs page of a slug and keeps its class", async () => {
		await renderWithProviders(
			<RouterDocLink slug="operations/backups" className="card">
				Backups
			</RouterDocLink>,
		);
		const link = screen.getByRole("link", { name: "Backups" });
		expect(link).toHaveAttribute("href", "/docs/operations/backups");
		expect(link).toHaveClass("card");
	});
});
