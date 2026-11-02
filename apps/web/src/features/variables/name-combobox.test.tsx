import { describe, expect, it } from "bun:test";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { ServiceView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	type UseNameComboboxOptions,
	useNameCombobox,
	useNameSources,
} from "./name-combobox";
import type { NameSuggestion } from "./name-suggestions";

// Literal `${NAME}` placeholder: the escaped `$` keeps it from being a template placeholder.
const ref = (name: string) => `\${${name}}`;

type Sources = UseNameComboboxOptions["sources"];

const SOURCES: Sources = {
	serviceKind: "app",
	services: [
		{
			slug: "postgres",
			name: "Postgres",
			exports: [
				{ key: "DATABASE_URL", description: "Connection URL", secret: true },
				{ key: "PGHOST", secret: false },
			],
		},
	],
	shared: [{ key: "LOG_LEVEL", layer: "project", secret: false }],
	team: [{ key: "SENTRY_DSN", secret: true }],
	envExample: [{ key: "STRIPE_KEY", needsInput: true, proposedSecret: true }],
};

interface HarnessProps {
	sources?: Sources;
	existingKeys?: readonly string[];
	enabled?: boolean;
	initial?: string;
	picked?: NameSuggestion[];
	outerKeys?: string[];
}

function Harness({
	sources = SOURCES,
	existingKeys = [],
	enabled = true,
	initial = "",
	picked = [],
	outerKeys = [],
}: HarnessProps) {
	const [value, setValue] = useState(initial);
	const name = useNameCombobox({
		value,
		onValueChange: setValue,
		sources,
		existingKeys,
		enabled,
		onPick: (suggestion) => {
			picked.push(suggestion);
			setValue(suggestion.name);
		},
	});
	return (
		// biome-ignore lint/a11y/noStaticElementInteractions: records keys that bubble out of the field
		<div className="relative" onKeyDown={(event) => outerKeys.push(event.key)}>
			<input aria-label="Name" value={value} {...name.fieldProps} />
			{name.popup}
			<button type="button">after</button>
		</div>
	);
}

const field = () => screen.getByRole("combobox", { name: "Name" });
const options = () => screen.queryAllByRole("option");
const optionNames = () => options().map((o) => o.textContent ?? "");
const selected = () =>
	options().find((o) => o.getAttribute("aria-selected") === "true");

describe("useNameCombobox", () => {
	it("stays closed until the field is focused", async () => {
		render(<Harness />);
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field()).toHaveAttribute("aria-expanded", "false");
		expect(field()).not.toHaveAttribute("aria-controls");
		const user = userEvent.setup();
		await user.click(field());
		const list = screen.getByRole("listbox", { name: "Variable names" });
		expect(field()).toHaveAttribute("aria-expanded", "true");
		expect(field()).toHaveAttribute("aria-controls", list.id);
	});

	it("offers every source with where it comes from and a secret marker", async () => {
		render(<Harness />);
		await userEvent.setup().click(field());
		const byName = (name: string) =>
			options().find((o) => o.textContent?.startsWith(name));
		expect(byName("DATABASE_URL")).toHaveTextContent("· from postgres");
		expect(byName("DATABASE_URL")).toHaveTextContent("secret");
		expect(byName("DATABASE_URL")).toHaveAttribute("title", "Connection URL");
		expect(byName("PGHOST")).not.toHaveTextContent("secret");
		expect(byName("LOG_LEVEL")).toHaveTextContent("· shared (project)");
		expect(byName("SENTRY_DSN")).toHaveTextContent("· team");
		expect(byName("STRIPE_KEY")).toHaveTextContent("· from .env.example");
		expect(byName("NODE_ENV")).toHaveTextContent("· common");
	});

	it("announces how many suggestions there are", async () => {
		render(
			<Harness
				sources={{
					...SOURCES,
					services: [],
					shared: [],
					team: [],
					envExample: [],
				}}
				initial="TZ"
			/>,
		);
		await userEvent.setup().click(field());
		expect(screen.getByRole("status")).toHaveTextContent("1 suggestion");
		fireEvent.change(field(), { target: { value: "L" } });
		expect(screen.getByRole("status")).toHaveTextContent(/\d+ suggestions/);
	});

	it("keeps no entry active while the field is empty, so Tab still leaves it", async () => {
		const picked: NameSuggestion[] = [];
		render(<Harness picked={picked} />);
		const user = userEvent.setup();
		await user.click(field());
		expect(selected()).toBeUndefined();
		expect(field()).not.toHaveAttribute("aria-activedescendant");
		await user.tab();
		expect(picked).toEqual([]);
		expect(screen.getByRole("button", { name: "after" })).toHaveFocus();
	});

	it("ArrowDown from an empty field activates the first entry and ArrowUp the last", async () => {
		render(<Harness />);
		const user = userEvent.setup();
		await user.click(field());
		const all = optionNames();
		await user.keyboard("{ArrowDown}");
		expect(options().indexOf(selected() as HTMLElement)).toBe(0);
		expect(field()).toHaveAttribute(
			"aria-activedescendant",
			selected()?.id ?? "",
		);
		// ArrowUp from the first wraps to the last.
		await user.keyboard("{ArrowUp}");
		expect(options().indexOf(selected() as HTMLElement)).toBe(all.length - 1);
		await user.keyboard("{ArrowDown}");
		expect(options().indexOf(selected() as HTMLElement)).toBe(0);
	});

	it("starts typed text on its best match and filters the list", async () => {
		render(<Harness />);
		const user = userEvent.setup();
		await user.click(field());
		await user.type(field(), "log");
		expect(optionNames()).toEqual(["LOG_LEVEL · shared (project)"]);
		expect(selected()).toHaveTextContent("LOG_LEVEL");
		// The typed text is emphasised inside the name.
		expect(selected()?.querySelector(".font-semibold")).toHaveTextContent(
			"LOG",
		);
	});

	it("Enter picks the active entry, closes the list and reports the suggestion", async () => {
		const picked: NameSuggestion[] = [];
		render(<Harness picked={picked} />);
		const user = userEvent.setup();
		await user.click(field());
		await user.type(field(), "pg");
		await user.keyboard("{Enter}");
		expect(picked).toHaveLength(1);
		expect(picked[0]).toMatchObject({
			name: "PGHOST",
			source: "export",
			from: "postgres",
			value: ref("{postgres.PGHOST}"),
		});
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field()).toHaveValue("PGHOST");
	});

	it("Tab picks the active entry instead of moving focus", async () => {
		const picked: NameSuggestion[] = [];
		render(<Harness picked={picked} />);
		const user = userEvent.setup();
		await user.click(field());
		await user.type(field(), "stripe");
		await user.tab();
		expect(picked.map((p) => p.name)).toEqual(["STRIPE_KEY"]);
		expect(field()).toHaveFocus();
	});

	it("Shift+Tab keeps its job and leaves the field without picking", async () => {
		const picked: NameSuggestion[] = [];
		render(<Harness picked={picked} />);
		const user = userEvent.setup();
		await user.click(field());
		await user.type(field(), "stripe");
		await user.tab({ shift: true });
		expect(picked).toEqual([]);
		expect(field()).not.toHaveFocus();
	});

	it("Enter without an active entry does nothing special", async () => {
		const picked: NameSuggestion[] = [];
		render(<Harness picked={picked} />);
		const user = userEvent.setup();
		await user.click(field());
		await user.keyboard("{Enter}");
		expect(picked).toEqual([]);
	});

	it("arrows skip entries that cannot be used", async () => {
		render(<Harness existingKeys={["LOG_LEVEL"]} />);
		const user = userEvent.setup();
		await user.click(field());
		await user.type(field(), "LOG");
		// LOG_LEVEL is already set: listed disabled, last, and never active.
		const disabled = options().find((o) =>
			o.textContent?.startsWith("LOG_LEVEL"),
		);
		expect(disabled).toHaveAttribute("aria-disabled", "true");
		expect(disabled).toHaveTextContent("Already set");
		expect(selected()).not.toBe(disabled);
	});

	it("does not pick a disabled entry on click", async () => {
		const picked: NameSuggestion[] = [];
		render(<Harness existingKeys={["LOG_LEVEL"]} picked={picked} />);
		const user = userEvent.setup();
		await user.click(field());
		await user.type(field(), "LOG_LEVEL");
		const disabled = options().find(
			(o) => o.getAttribute("aria-disabled") === "true",
		);
		expect(disabled).toBeDefined();
		await user.click(disabled as HTMLElement);
		expect(picked).toEqual([]);
		await user.keyboard("{Enter}");
		// Nothing usable matched, so Enter picks nothing either.
		expect(picked).toEqual([]);
	});

	it("says a platform name is provided by the platform", async () => {
		render(<Harness />);
		const user = userEvent.setup();
		await user.click(field());
		await user.type(field(), "PORT");
		const port = options().find((o) => o.textContent?.startsWith("PORT"));
		expect(port).toHaveAttribute("aria-disabled", "true");
		expect(port).toHaveTextContent("Provided by the platform");
	});

	it("clicking an entry picks it without taking focus from the field", async () => {
		const picked: NameSuggestion[] = [];
		render(<Harness picked={picked} />);
		const user = userEvent.setup();
		await user.click(field());
		await user.type(field(), "sentry");
		const option = within(screen.getByRole("listbox")).getAllByRole(
			"option",
		)[0];
		expect(option).toHaveTextContent("SENTRY_DSN");
		await user.click(option as HTMLElement);
		expect(picked.map((p) => p.name)).toEqual(["SENTRY_DSN"]);
		expect(picked[0]?.value).toBe(ref("{team.SENTRY_DSN}"));
		expect(field()).toHaveFocus();
	});

	it("hovering moves the active entry", async () => {
		render(<Harness />);
		const user = userEvent.setup();
		await user.click(field());
		await user.keyboard("{ArrowDown}");
		const last = options().at(-1) as HTMLElement;
		await user.hover(last);
		expect(selected()).toBe(last);
	});

	it("Escape closes the list without letting the key reach the dialog around it", async () => {
		const outerKeys: string[] = [];
		render(<Harness outerKeys={outerKeys} />);
		const user = userEvent.setup();
		await user.click(field());
		expect(screen.getByRole("listbox")).toBeInTheDocument();
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(outerKeys).not.toContain("Escape");
		// With the list closed, Escape is no longer swallowed.
		await user.keyboard("{Escape}");
		expect(outerKeys).toContain("Escape");
	});

	it("ArrowDown reopens a dismissed list, and typing reopens it too", async () => {
		render(<Harness />);
		const user = userEvent.setup();
		await user.click(field());
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("listbox")).toBeNull();
		await user.keyboard("{ArrowDown}");
		expect(screen.getByRole("listbox")).toBeInTheDocument();
		await user.keyboard("{Escape}");
		await user.type(field(), "l");
		expect(screen.getByRole("listbox")).toBeInTheDocument();
	});

	it("closes when the field loses focus", async () => {
		render(<Harness />);
		const user = userEvent.setup();
		await user.click(field());
		expect(screen.getByRole("listbox")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "after" }));
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("offers nothing for text that matches no name", async () => {
		render(<Harness />);
		const user = userEvent.setup();
		await user.click(field());
		await user.type(field(), "zzzzqqq");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field()).toHaveAttribute("aria-expanded", "false");
	});

	it("offers nothing while a reference is typed in the name", async () => {
		render(<Harness initial={`\${{post`} />);
		await userEvent.setup().click(field());
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("is inert when disabled: no list and its keys do nothing", async () => {
		const picked: NameSuggestion[] = [];
		render(<Harness enabled={false} picked={picked} />);
		const user = userEvent.setup();
		await user.click(field());
		await user.keyboard("{ArrowDown}{Enter}");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(picked).toEqual([]);
	});

	it("wraps ArrowDown from the last usable entry to the first", async () => {
		render(
			<Harness
				sources={{
					...SOURCES,
					services: [],
					shared: [],
					team: [],
					envExample: [],
				}}
				initial="L"
			/>,
		);
		const user = userEvent.setup();
		await user.click(field());
		// Platform names match "L" too, but are listed disabled and never active.
		const count = options().filter(
			(o) => o.getAttribute("aria-disabled") !== "true",
		).length;
		expect(count).toBeGreaterThan(1);
		expect(options().length).toBeGreaterThan(count);
		expect(options().indexOf(selected() as HTMLElement)).toBe(0);
		for (let i = 0; i < count; i += 1) await user.keyboard("{ArrowDown}");
		expect(options().indexOf(selected() as HTMLElement)).toBe(0);
	});
});

const serviceWire = (
	id: string,
	slug: string,
	kind: ServiceView["kind"] = "app",
) =>
	make("ServiceView", {
		id,
		environmentId: "env-1",
		projectId: "p1",
		name: slug.toUpperCase(),
		slug,
		kind,
		sourceType: "image",
	});

function SourcesProbe({
	environmentId = "env-1",
	serviceId = "svc-me",
	enabled = true,
}: {
	environmentId?: string;
	serviceId?: string;
	enabled?: boolean;
}) {
	const sources = useNameSources(environmentId, serviceId, enabled);
	return <output aria-label="sources">{JSON.stringify(sources)}</output>;
}

const sourcesOf = () =>
	JSON.parse(screen.getByLabelText("sources").textContent ?? "");

describe("useNameSources", () => {
	it("fetches nothing until enabled", async () => {
		const api = stubApi({});
		await renderWithProviders(<SourcesProbe enabled={false} />);
		expect(api.calls).toEqual([]);
		expect(sourcesOf()).toEqual({
			serviceKind: "app",
			services: [],
			shared: [],
			team: [],
			envExample: [],
		});
	});

	it("gathers the other services' exports, shared, team and .env.example names", async () => {
		const api = stubApi({
			"GET /environments/:id/services": {
				body: make("PageServiceView", {
					items: [
						serviceWire("svc-me", "api", "worker"),
						serviceWire("svc-db", "postgres", "database"),
					],
				}),
			},
			"GET /services/:id/variables": (request) =>
				Response.json(
					new URL(request.url).pathname.includes("svc-db")
						? make("VariableList", {
								exports: [
									{ key: "DATABASE_URL", description: "URL", secret: true },
									{ key: "PGHOST", description: "", secret: false },
								],
							})
						: make("VariableList"),
				),
			"GET /environments/:id/shared-variables": {
				body: make("SharedVariableList", {
					items: [
						make("SharedVariableView", {
							key: "LOG_LEVEL",
							layer: "project",
							computedSecret: false,
						}),
					],
				}),
			},
			"GET /team/shared-variables": {
				body: make("SharedVariableList", {
					items: [
						make("SharedVariableView", {
							key: "SENTRY_DSN",
							layer: "team",
							computedSecret: true,
						}),
					],
				}),
			},
			"GET /services/:id/suggested-variables": {
				body: make("SuggestedVariableList", {
					items: [
						make("SuggestedVariable", {
							key: "STRIPE_KEY",
							value: "",
							needsInput: true,
							proposedSecret: true,
							source: "env-example",
						}),
						make("SuggestedVariable", {
							key: "DATABASE_URL",
							value: "x",
							source: "database",
						}),
					],
				}),
			},
		});
		await renderWithProviders(<SourcesProbe />);
		await waitFor(() => expect(sourcesOf().services).toHaveLength(1));
		await waitFor(() =>
			expect(sourcesOf().services[0].exports).toHaveLength(2),
		);
		await waitFor(() => expect(sourcesOf().envExample).toHaveLength(1));
		await waitFor(() => expect(sourcesOf().team).toHaveLength(1));
		await waitFor(() => expect(sourcesOf().shared).toHaveLength(1));
		const sources = sourcesOf();
		// The kind is the edited service's own.
		expect(sources.serviceKind).toBe("worker");
		// The edited service is left out; only the other one offers exports.
		expect(sources.services).toEqual([
			{
				slug: "postgres",
				name: "POSTGRES",
				exports: [
					{ key: "DATABASE_URL", description: "URL", secret: true },
					{ key: "PGHOST", secret: false },
				],
			},
		]);
		expect(sources.shared).toEqual([
			{ key: "LOG_LEVEL", layer: "project", secret: false },
		]);
		expect(sources.team).toEqual([{ key: "SENTRY_DSN", secret: true }]);
		// Only `.env.example` keys count; a database suggestion is not a name source.
		expect(sources.envExample).toEqual([
			{ key: "STRIPE_KEY", value: "", needsInput: true, proposedSecret: true },
		]);
		// No variable of the edited service is read.
		expect(api.calls.map((c) => c.path)).not.toContain(
			"/services/svc-me/variables",
		);
		expect(api.unmatched).toEqual([]);
	});

	it("still lists services when the team variables are forbidden", async () => {
		stubApi({
			"GET /environments/:id/services": {
				body: make("PageServiceView", {
					items: [
						serviceWire("svc-me", "api"),
						serviceWire("svc-db", "postgres"),
					],
				}),
			},
			"GET /services/:id/variables": {
				body: make("VariableList", {
					exports: [{ key: "PGHOST", description: "", secret: false }],
				}),
			},
			"GET /environments/:id/shared-variables": {
				body: make("SharedVariableList"),
			},
			"GET /team/shared-variables": problem(403, {
				code: "forbidden",
				title: "Forbidden",
			}),
			"GET /services/:id/suggested-variables": {
				body: make("SuggestedVariableList"),
			},
		});
		await renderWithProviders(<SourcesProbe />);
		await waitFor(() =>
			expect(sourcesOf().services[0]?.exports).toHaveLength(1),
		);
		expect(sourcesOf().team).toEqual([]);
	});
});
