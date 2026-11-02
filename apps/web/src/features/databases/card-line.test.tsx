import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { DatabaseLine } from "./card-line";
import { catalogBody, problem } from "./database.fixture";

const CATALOG = "GET /database-engines";

describe("DatabaseLine", () => {
	test("shows the engine's catalog label and the version once the catalog is loaded", async () => {
		stubApi({ [CATALOG]: { body: catalogBody } });
		await renderWithProviders(
			<DatabaseLine spec={{ engine: "postgres", version: "16" }} />,
		);
		expect(await screen.findByText("PostgreSQL")).toBeVisible();
		expect(screen.getByText("v16")).toBeVisible();
		expect(screen.getByTitle("PostgreSQL v16")).toBeInTheDocument();
	});

	test("until the catalog loads the engine id stands in for the label", async () => {
		stubApi({ [CATALOG]: () => new Promise<Response>(() => {}) });
		await renderWithProviders(
			<DatabaseLine spec={{ engine: "postgres", version: "16" }} />,
		);
		expect(screen.getByText("postgres")).toBeVisible();
		expect(screen.getByText("v16")).toBeVisible();
		expect(screen.getByTitle("postgres v16")).toBeInTheDocument();
	});

	test("an engine the catalog does not list keeps its id and shows the raw flavor", async () => {
		stubApi({ [CATALOG]: { body: catalogBody } });
		await renderWithProviders(
			<DatabaseLine
				spec={{ engine: "cockroach", version: "24", flavor: "edge" }}
			/>,
		);
		expect(await screen.findByText("v24 · edge")).toBeVisible();
		expect(screen.getByText("cockroach")).toBeVisible();
	});

	test("a known flavor shows its catalog label", async () => {
		stubApi({ [CATALOG]: { body: catalogBody } });
		await renderWithProviders(
			<DatabaseLine
				spec={{ engine: "postgres", version: "17", flavor: "postgis" }}
			/>,
		);
		expect(await screen.findByText("v17 · PostGIS")).toBeVisible();
		expect(screen.getByTitle("PostgreSQL v17 · PostGIS")).toBeInTheDocument();
	});

	test("a catalog that fails to load leaves the id and the raw flavor", async () => {
		const api = stubApi({
			[CATALOG]: problem(500, "catalog down", "internal"),
		});
		await renderWithProviders(
			<DatabaseLine
				spec={{ engine: "postgres", version: "17", flavor: "postgis" }}
			/>,
		);
		expect(await screen.findByText("v17 · postgis")).toBeVisible();
		expect(screen.getByText("postgres")).toBeVisible();
		expect(api.calls).toHaveLength(1);
	});
});
