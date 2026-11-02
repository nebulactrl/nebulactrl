import { describe, expect, it } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { toClonePlan, toMovePlan } from "./model";
import { ClonePlanView, MovePlanView } from "./plan-view";
import { cloneResult, moveResult } from "./test-fixtures";

describe("ClonePlanView", () => {
	it("says nothing for an empty plan", () => {
		const { container } = render(
			<ClonePlanView plan={toClonePlan(cloneResult())} showServices />,
		);
		expect(container.textContent).toBe("");
	});

	it("lists every service of an environment clone, flagging fresh databases", () => {
		const plan = toClonePlan(
			cloneResult({
				services: [
					{
						id: "1",
						sourceId: "0",
						name: "API",
						slug: "api",
						kind: "app",
						fresh: false,
					},
					{
						id: "2",
						sourceId: "9",
						name: "Postgres",
						slug: "postgres",
						kind: "database",
						fresh: true,
					},
				],
			}),
		);
		render(<ClonePlanView plan={plan} showServices />);
		expect(screen.getByText("Services (2)")).toBeInTheDocument();
		expect(screen.getByText("api · app")).toBeInTheDocument();
		expect(
			screen.getByText("postgres · database · new, empty database"),
		).toBeInTheDocument();
	});

	it("leaves the service list out of a single-service clone", () => {
		const plan = toClonePlan(
			cloneResult({
				services: [
					{
						id: "1",
						sourceId: "0",
						name: "API",
						slug: "api",
						kind: "app",
						fresh: false,
					},
				],
			}),
		);
		render(<ClonePlanView plan={plan} showServices={false} />);
		expect(screen.queryByText(/^Services/)).not.toBeInTheDocument();
		expect(screen.queryByText("api · app")).not.toBeInTheDocument();
	});

	it("explains each domain outcome in words", () => {
		const plan = toClonePlan(
			cloneResult({
				domains: [
					{ service: "api", hostname: "a.example.com", action: "dropped" },
					{
						service: "api",
						hostname: "b.example.com",
						action: "suffixed",
						to: "b-copy.example.com",
					},
					{
						service: "api",
						hostname: "c.example.com",
						action: "skipped",
						reason: "apex name",
					},
				],
			}),
		);
		render(<ClonePlanView plan={plan} showServices />);
		const domains = screen.getByRole("heading", { name: "Domains" })
			.parentElement as HTMLElement;
		const items = within(domains)
			.getAllByRole("listitem")
			.map((li) => li.textContent);
		expect(items).toEqual([
			"a.example.com → not cloned",
			"b.example.com → b-copy.example.com",
			"c.example.com → apex name",
		]);
	});

	it("blocks with a single-host title when one hostname conflicts", () => {
		const plan = toClonePlan(
			cloneResult({
				domains: [
					{
						service: "api",
						hostname: "app.example.com",
						action: "conflict",
						to: "app-copy.example.com",
					},
				],
			}),
		);
		render(<ClonePlanView plan={plan} showServices />);
		expect(screen.getByText("A hostname is already in use")).toBeVisible();
		expect(
			screen.getByText(/app-copy\.example\.com\. Choose another suffix/),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"app.example.com → app-copy.example.com is already in use",
			),
		).toBeInTheDocument();
	});

	it("uses the plural title and joins every conflicting host", () => {
		const plan = toClonePlan(
			cloneResult({
				domains: [
					{
						service: "api",
						hostname: "a.example.com",
						action: "conflict",
						to: "a-x.example.com",
					},
					{ service: "web", hostname: "b.example.com", action: "conflict" },
				],
			}),
		);
		render(<ClonePlanView plan={plan} showServices />);
		expect(screen.getByText("Some hostnames are already in use")).toBeVisible();
		expect(
			screen.getByText(/a-x\.example\.com, b\.example\.com\. Choose another/),
		).toBeInTheDocument();
	});

	it("warns about references that do not resolve and lists skipped services", () => {
		const plan = toClonePlan(
			cloneResult({
				references: [
					{
						service: "api",
						key: "DATABASE_URL",
						ref: "postgres.DATABASE_URL",
						status: "unresolved",
						message: "no such service",
					},
				],
				skipped: [{ slug: "redis", reason: "shared service" }],
			}),
		);
		render(<ClonePlanView plan={plan} showServices />);
		expect(
			screen.getByText("References that do not resolve there"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"api · DATABASE_URL → postgres.DATABASE_URL: no such service",
			),
		).toBeInTheDocument();
		expect(screen.getByText("Not cloned")).toBeInTheDocument();
		expect(screen.getByText("redis: shared service")).toBeInTheDocument();
	});

	it.each([
		[2, 0, "2 link(s) copied."],
		[0, 3, "3 shared variable(s) copied."],
		[1, 4, "1 link(s) and 4 shared variable(s) copied."],
	])("summarises %i links and %i shared variables", (links, shared, text) => {
		const plan = toClonePlan(cloneResult({ links, sharedVariables: shared }));
		render(<ClonePlanView plan={plan} showServices />);
		expect(screen.getByText(text)).toBeInTheDocument();
	});

	it("shows warnings verbatim", () => {
		const plan = toClonePlan(
			cloneResult({ warnings: ["Volumes start empty", "Check your cron"] }),
		);
		render(<ClonePlanView plan={plan} showServices />);
		expect(screen.getByText("Volumes start empty")).toBeInTheDocument();
		expect(screen.getByText("Check your cron")).toBeInTheDocument();
	});
});

describe("MovePlanView", () => {
	it("says nothing breaks for a clean plan", () => {
		render(<MovePlanView plan={toMovePlan(moveResult())} />);
		expect(screen.getByText("Nothing breaks.")).toBeInTheDocument();
		expect(screen.queryByText(/Move it anyway/)).not.toBeInTheDocument();
	});

	it("explains how a blocked move can go ahead", () => {
		const plan = toMovePlan(
			moveResult({
				blocked: true,
				outgoing: [
					{
						service: "api",
						key: "REDIS_URL",
						ref: "redis.URL",
						status: "unresolved",
						message: "not in Staging",
					},
				],
				incoming: [{ service: "worker", keys: ["API_URL", "API_HOST"] }],
			}),
		);
		render(<MovePlanView plan={plan} />);
		expect(screen.getByText("This move breaks 2 references.")).toBeVisible();
		expect(
			screen.getByText(/Move it anyway and fix them afterwards/),
		).toBeVisible();
		expect(screen.getByText("Variables of this service")).toBeInTheDocument();
		expect(
			screen.getByText("api · REDIS_URL → redis.URL: not in Staging"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Other services that reference it"),
		).toBeInTheDocument();
		expect(screen.getByText("worker · API_URL, API_HOST")).toBeInTheDocument();
	});

	it("lists removed links, warnings and the new slug when it changed", () => {
		const plan = toMovePlan(
			moveResult({
				links: ["api → postgres"],
				warnings: ["Domains stay with the service"],
				slugChanged: true,
				slug: "api-2",
				to: {
					id: "e",
					name: "Staging",
					slug: "staging",
					projectId: "p",
					isProduction: false,
				},
			}),
		);
		render(<MovePlanView plan={plan} />);
		expect(
			screen.getByText("Nothing breaks. Review the notes below."),
		).toBeInTheDocument();
		expect(screen.getByText("Links removed")).toBeInTheDocument();
		expect(screen.getByText("api → postgres")).toBeInTheDocument();
		expect(
			screen.getByText("Domains stay with the service"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/Staging already has a service with this name: it becomes/,
			),
		).toHaveTextContent("it becomes api-2.");
	});
});
