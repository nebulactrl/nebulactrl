import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { TemplateCard } from "./template-card";
import { summary } from "./test-fixtures";

const renderCard = (item: Parameters<typeof summary>[0], onOpen = mock()) =>
	renderWithProviders(
		<ul>
			<TemplateCard item={summary(item)} onOpen={onOpen} />
		</ul>,
	).then(() => onOpen);

describe("TemplateCard", () => {
	test("shows name, summary, size and licence", async () => {
		await renderCard({
			id: "umami",
			name: "Umami",
			summary: "Privacy-friendly analytics",
			services: 2,
			memoryMb: 1536,
			license: "MIT",
		});
		expect(screen.getByText("Umami")).toBeInTheDocument();
		expect(screen.getByText("Privacy-friendly analytics")).toBeInTheDocument();
		expect(screen.getByText("2 services · ~1.5 GB")).toBeInTheDocument();
		expect(screen.getByText("MIT")).toBeInTheDocument();
	});

	test("one service is singular and no memory estimate is omitted", async () => {
		await renderCard({ id: "x", services: 1, memoryMb: 0 });
		expect(screen.getByText("1 service")).toBeInTheDocument();
	});

	test("clicking the card opens that template", async () => {
		const user = userEvent.setup();
		const onOpen = await renderCard({
			id: "umami",
			name: "Umami",
			summary: "Analytics",
		});
		await user.click(screen.getByRole("button", { name: "Umami: Analytics" }));
		expect(onOpen).toHaveBeenCalledTimes(1);
		expect(onOpen).toHaveBeenCalledWith("umami");
	});

	test("shows the first database engine by name", async () => {
		await renderCard({ id: "x", databases: ["postgres 17"] });
		expect(screen.getByText("PostgreSQL 17")).toBeInTheDocument();
		expect(screen.queryByText(/^\+\d/)).not.toBeInTheDocument();
	});

	test("repeated engines count once; extra engines collapse into +N with the full list as title", async () => {
		await renderCard({
			id: "x",
			databases: ["postgres 17", "postgres 17", "redis", "mysql"],
		});
		expect(screen.getByText("PostgreSQL 17")).toBeInTheDocument();
		const more = screen.getByText("+2");
		expect(more).toHaveAttribute("title", "PostgreSQL 17, Redis, MySQL");
	});

	test("a card with no databases has no engine badge", async () => {
		await renderCard({ id: "x", databases: null });
		const card = screen.getByRole("listitem");
		expect(within(card).queryByText(/PostgreSQL|\+\d/)).not.toBeInTheDocument();
	});

	test("a heavy template carries a Heavy badge that explains itself on focus", async () => {
		const user = userEvent.setup();
		await renderCard({ id: "x", heavy: true });
		expect(screen.getByText("Heavy")).toBeInTheDocument();
		await user.tab(); // the card button
		await user.tab(); // Heavy
		expect(
			await screen.findByText(/Several GB to download/),
		).toBeInTheDocument();
	});

	test("a light template has no Heavy badge", async () => {
		await renderCard({ id: "x" });
		expect(screen.queryByText("Heavy")).not.toBeInTheDocument();
	});

	test("a template that publishes ports says TCP/UDP ports", async () => {
		const user = userEvent.setup();
		await renderCard({ id: "mc", publishesPorts: true });
		expect(screen.getByText("TCP/UDP ports")).toBeInTheDocument();
		await user.tab();
		await user.tab();
		expect(
			await screen.findByText(/public interface for players to connect/),
		).toBeInTheDocument();
	});

	test("a template without published ports has no port badge", async () => {
		await renderCard({ id: "x", publishesPorts: false });
		expect(screen.queryByText("TCP/UDP ports")).not.toBeInTheDocument();
	});

	test("marks templates that need an unrestricted server, and only those", async () => {
		await renderCard({ id: "gpu", requiresUnrestricted: true });
		expect(
			screen.getByText("Needs an unrestricted server"),
		).toBeInTheDocument();
	});

	test("a template that does not need the tier has no such badge", async () => {
		await renderCard({ id: "plain" });
		expect(
			screen.queryByText("Needs an unrestricted server"),
		).not.toBeInTheDocument();
	});

	test("a source-available licence is flagged on the card", async () => {
		await renderCard({
			id: "x",
			license: "BUSL-1.1",
			licenseClass: "source-available",
		});
		expect(screen.getByText("Source-available")).toBeInTheDocument();
	});
});
