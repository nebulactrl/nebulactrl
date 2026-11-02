import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import { ReplicaBackupsNote } from "./backups-note";

describe("ReplicaBackupsNote", () => {
	test("links the primary's Backups tab when the environment is in the URL", async () => {
		await renderWithProviders(
			<ReplicaBackupsNote primary={{ id: "svc-p", slug: "orders-db" }} />,
			{ path: "/p/proj-1/prod", routePath: "/p/$projectId/$envSlug" },
		);
		expect(screen.getByText("Backups run on the primary")).toBeInTheDocument();
		const link = screen.getByRole("link", { name: "orders-db" });
		const href = new URL(link.getAttribute("href") ?? "", "http://x");
		expect(href.pathname).toBe("/p/proj-1/prod");
		expect(href.searchParams.get("service")).toBe("svc-p");
		expect(href.searchParams.get("tab")).toBe("backups");
	});

	test("names the primary as plain text when there is no environment in the URL", async () => {
		await renderWithProviders(
			<ReplicaBackupsNote primary={{ id: "svc-p", slug: "orders-db" }} />,
			{ path: "/somewhere" },
		);
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
		expect(screen.getByText(/belong to orders-db\./)).toBeInTheDocument();
	});

	test("falls back to 'the primary' when the primary is unknown", async () => {
		await renderWithProviders(<ReplicaBackupsNote primary={null} />, {
			path: "/p/proj-1/prod",
			routePath: "/p/$projectId/$envSlug",
		});
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
		expect(screen.getByText(/belong to the primary\./)).toBeInTheDocument();
		expect(
			screen.getByText(/the schedules move with the primary role/),
		).toBeInTheDocument();
	});
});
