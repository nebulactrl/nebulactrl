import { describe, expect, it, mock } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { StatusPageView } from "#/api/gen/types.gen";
import type { ServiceOption } from "./model";
import { PageChecklist, ServiceChecklist } from "./service-checklist";

function service(
	id: string,
	name: string,
	projectName = "Acme",
	environmentName = "production",
): ServiceOption {
	return { id, name, projectName, environmentName };
}

const THREE = [
	service("web", "web"),
	service("api", "api"),
	service("worker", "worker", "Acme", "staging"),
];

function list(extra: Partial<Parameters<typeof ServiceChecklist>[0]> = {}) {
	return (
		<ServiceChecklist
			label="Services to add"
			services={THREE}
			pending={false}
			selected={[]}
			onToggle={() => {}}
			{...extra}
		/>
	);
}

describe("ServiceChecklist", () => {
	it("groups services under 'Project · environment' headings", () => {
		render(list());
		const production = screen.getByRole("group", { name: "Acme · production" });
		expect(within(production).getAllByRole("checkbox")).toHaveLength(2);
		expect(
			within(production).getByRole("checkbox", { name: "web" }),
		).toBeInTheDocument();
		expect(
			within(production).getByRole("checkbox", { name: "api" }),
		).toBeInTheDocument();
		const staging = screen.getByRole("group", { name: "Acme · staging" });
		expect(
			within(staging).getByRole("checkbox", { name: "worker" }),
		).toBeInTheDocument();
	});

	it("names the whole list for assistive tech", () => {
		render(list());
		expect(
			screen.getByRole("group", { name: "Services to add" }),
		).toBeInTheDocument();
	});

	it("ticks the selected services only", () => {
		render(list({ selected: ["api"] }));
		expect(screen.getByRole("checkbox", { name: "api" })).toBeChecked();
		expect(screen.getByRole("checkbox", { name: "web" })).not.toBeChecked();
	});

	it("reports the service and the new state when a box is toggled", async () => {
		const onToggle = mock((_s: ServiceOption, _on: boolean) => {});
		const user = userEvent.setup();
		const { rerender } = render(list({ onToggle }));
		await user.click(screen.getByRole("checkbox", { name: "web" }));
		expect(onToggle).toHaveBeenLastCalledWith(THREE[0], true);

		rerender(list({ onToggle, selected: ["web"] }));
		await user.click(screen.getByRole("checkbox", { name: "web" }));
		expect(onToggle).toHaveBeenLastCalledWith(THREE[0], false);
	});

	it("clicking the label text toggles the box", async () => {
		const onToggle = mock((_s: ServiceOption, _on: boolean) => {});
		render(list({ onToggle }));
		await userEvent.setup().click(screen.getByText("worker"));
		expect(onToggle).toHaveBeenCalledWith(THREE[2], true);
	});

	it("does not offer services in `exclude`", () => {
		render(list({ exclude: new Set(["web"]) }));
		expect(
			screen.queryByRole("checkbox", { name: "web" }),
		).not.toBeInTheDocument();
		expect(screen.getByRole("checkbox", { name: "api" })).toBeInTheDocument();
	});

	it("says every service is already on the page once all are excluded", () => {
		render(list({ exclude: new Set(["web", "api", "worker"]) }));
		expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
		expect(
			screen.getByText("Every service is already on the page."),
		).toBeInTheDocument();
	});

	it("shows the default empty text for a team with no services", () => {
		render(list({ services: [] }));
		expect(
			screen.getByText(
				"No services yet. Create one on a project's canvas first.",
			),
		).toBeInTheDocument();
	});

	it("shows a caller's own empty text", () => {
		render(list({ services: [], emptyText: "Nothing to pick." }));
		expect(screen.getByText("Nothing to pick.")).toBeInTheDocument();
	});

	it("says it is loading while the services load", () => {
		render(list({ services: [], pending: true }));
		expect(screen.getByText("Loading services…")).toBeInTheDocument();
	});

	it("has no search box for a short list", () => {
		render(list());
		expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
	});

	describe("with a long list", () => {
		const MANY = [
			service("s1", "web"),
			service("s2", "api"),
			service("s3", "billing"),
			service("s4", "checkout"),
			service("s5", "search"),
			service("s6", "mailer", "Internal", "production"),
		];

		it("filters by service, project or environment and says when nothing matches", async () => {
			const user = userEvent.setup();
			render(list({ services: MANY }));
			const box = screen.getByRole("searchbox", {
				name: "Search services to add",
			});

			await user.type(box, "internal");
			expect(screen.getAllByRole("checkbox")).toHaveLength(1);
			expect(
				screen.getByRole("checkbox", { name: "mailer" }),
			).toBeInTheDocument();

			await user.clear(box);
			await user.type(box, "zzz");
			expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
			expect(
				screen.getByText("No service matches the search."),
			).toBeInTheDocument();
		});

		it("counts the offered services, not the excluded ones, for the search box", () => {
			const { unmount } = render(list({ services: MANY }));
			expect(screen.getByRole("searchbox")).toBeInTheDocument();
			unmount();
			render(list({ services: MANY, exclude: new Set(["s1"]) }));
			expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
		});

		it("keeps the search box when a query matches nothing, so it can be cleared", async () => {
			const user = userEvent.setup();
			render(list({ services: MANY }));
			await user.type(screen.getByRole("searchbox"), "zzz");
			expect(screen.getByRole("searchbox")).toHaveValue("zzz");
			await user.clear(screen.getByRole("searchbox"));
			expect(screen.getAllByRole("checkbox")).toHaveLength(6);
		});
	});
});

function page(over: Partial<StatusPageView> = {}): StatusPageView {
	return {
		id: "page-1",
		title: "Acme status",
		slug: "acme-status",
		description: "",
		footer: "",
		showUptime: true,
		hostname: "status.example.com",
		published: true,
		components: [],
		createdAt: "2026-10-01T00:00:00Z",
		updatedAt: "2026-10-01T00:00:00Z",
		...over,
	};
}

describe("PageChecklist", () => {
	it("lists each page with its hostname, marking drafts and pages without a hostname", () => {
		render(
			<PageChecklist
				label="Pages"
				pending={false}
				selected={[]}
				onToggle={() => {}}
				pages={[
					page(),
					page({
						id: "page-2",
						title: "Beta",
						hostname: null,
						published: false,
					}),
					page({
						id: "page-3",
						title: "Gamma",
						hostname: "g.example.com",
						published: false,
					}),
				]}
			/>,
		);
		const acme = screen.getByRole("checkbox", { name: /Acme status/ });
		expect(acme).toBeInTheDocument();
		expect(screen.getByText("status.example.com")).toBeInTheDocument();
		expect(screen.getByText("no hostname yet · draft")).toBeInTheDocument();
		expect(screen.getByText("g.example.com · draft")).toBeInTheDocument();
	});

	it("ticks the selected pages and reports toggles by page id", async () => {
		const onToggle = mock((_id: string, _on: boolean) => {});
		const user = userEvent.setup();
		render(
			<PageChecklist
				label="Pages"
				pending={false}
				selected={["page-2"]}
				onToggle={onToggle}
				pages={[page(), page({ id: "page-2", title: "Beta" })]}
			/>,
		);
		expect(screen.getByRole("checkbox", { name: /Beta/ })).toBeChecked();
		expect(
			screen.getByRole("checkbox", { name: /Acme status/ }),
		).not.toBeChecked();
		await user.click(screen.getByRole("checkbox", { name: /Acme status/ }));
		expect(onToggle).toHaveBeenLastCalledWith("page-1", true);
		await user.click(screen.getByRole("checkbox", { name: /Beta/ }));
		expect(onToggle).toHaveBeenLastCalledWith("page-2", false);
	});

	it("says there are no status pages, or that they are loading", () => {
		const { rerender } = render(
			<PageChecklist
				label="Pages"
				pending={false}
				selected={[]}
				onToggle={() => {}}
				pages={[]}
			/>,
		);
		expect(screen.getByText("No status pages yet.")).toBeInTheDocument();
		rerender(
			<PageChecklist
				label="Pages"
				pending
				selected={[]}
				onToggle={() => {}}
				pages={[]}
			/>,
		);
		expect(screen.getByText("Loading pages…")).toBeInTheDocument();
	});
});
