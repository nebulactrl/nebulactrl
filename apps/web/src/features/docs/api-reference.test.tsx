// Tests of @nebulactrl/docs/api-reference. They live here because this is the
// app that renders it, and so the package's React and DOM are the app's.
import { describe, expect, test } from "bun:test";
import {
	ApiTagList,
	ApiTagView,
	type DocLinkComponent,
	PlainDocLink,
} from "@nebulactrl/docs/api-reference";
import type {
	ApiOperation,
	ApiSchema,
	ApiTagPage,
	DocMeta,
} from "@nebulactrl/docs/types";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const meta = (slug: string, title: string, description: string): DocMeta => ({
	slug,
	title,
	description,
	section: "API",
	order: 1,
	kind: "api-tag",
});

function operation(extra: Partial<ApiOperation> = {}): ApiOperation {
	return {
		id: "getService",
		method: "GET",
		path: "/services/{serviceId}",
		summary: "Get a service",
		description: "",
		public: false,
		params: [],
		responses: [],
		errorCodes: [],
		...extra,
	};
}

function tagPage(
	operations: ApiOperation[],
	defs: Record<string, ApiSchema> = {},
	description = "Services of an environment",
): ApiTagPage {
	return {
		kind: "api-tag",
		slug: "reference/api/services",
		tag: "services",
		description,
		operations,
		defs,
	};
}

const SlugButtonLink: DocLinkComponent = ({ slug, children }) => (
	<button type="button" data-slug={slug}>
		{children}
	</button>
);

describe("PlainDocLink and ApiTagList", () => {
	test("each tag is a link to its docs page with its title and description", () => {
		render(
			<ApiTagList
				tags={[
					meta("reference/api/services", "Services", "Create and deploy"),
					meta("reference/api/auth", "Auth", "Sessions"),
				]}
			/>,
		);
		const services = screen.getByRole("link", { name: /Services/ });
		expect(services).toHaveAttribute("href", "/docs/reference/api/services");
		expect(within(services).getByText("Create and deploy")).toBeInTheDocument();
		expect(screen.getByRole("link", { name: /Auth/ })).toHaveAttribute(
			"href",
			"/docs/reference/api/auth",
		);
	});

	test("the router's own link component is used when given", () => {
		render(
			<ApiTagList
				tags={[meta("x/y", "Title", "Desc")]}
				Link={SlugButtonLink}
			/>,
		);
		expect(screen.getByRole("button", { name: /Title/ })).toHaveAttribute(
			"data-slug",
			"x/y",
		);
	});

	test("PlainDocLink links the docs root for an empty slug", () => {
		render(
			<PlainDocLink slug="" className="c">
				Docs
			</PlainDocLink>,
		);
		expect(screen.getByRole("link", { name: "Docs" })).toHaveAttribute(
			"href",
			"/docs",
		);
	});
});

describe("ApiTagView", () => {
	test("titles the page, counts operations and points to the conventions", () => {
		render(
			<ApiTagView
				title="Services"
				page={tagPage([
					operation(),
					operation({ id: "list", path: "/services" }),
				])}
			/>,
		);
		expect(
			screen.getByRole("heading", { level: 1, name: "Services API" }),
		).toBeInTheDocument();
		expect(screen.getByText("Services of an environment.")).toBeInTheDocument();
		expect(screen.getByText(/2 operations\./)).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Conventions, errors and scopes" }),
		).toHaveAttribute("href", "/docs/reference/api");
	});

	test("one operation reads in the singular, and a page without description omits it", () => {
		render(
			<ApiTagView title="Services" page={tagPage([operation()], {}, "")} />,
		);
		expect(screen.getByText(/1 operation\./)).toBeInTheDocument();
		expect(
			screen.queryByText(/Services of an environment/),
		).not.toBeInTheDocument();
	});

	test("an operation shows its method, path, summary and anchor", () => {
		const { container } = render(
			<ApiTagView
				title="Services"
				page={tagPage([operation({ id: "getService", method: "DELETE" })])}
			/>,
		);
		expect(container.querySelector("#op-getService")).not.toBeNull();
		expect(screen.getByText("DELETE")).toHaveAttribute("data-method", "DELETE");
		expect(screen.getByText("/services/{serviceId}")).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { level: 2, name: "Get a service" }),
		).toBeInTheDocument();
	});

	test("only public operations carry the Public badge", () => {
		const { rerender } = render(
			<ApiTagView title="S" page={tagPage([operation({ public: true })])} />,
		);
		expect(screen.getByText("Public")).toBeInTheDocument();
		rerender(
			<ApiTagView title="S" page={tagPage([operation({ public: false })])} />,
		);
		expect(screen.queryByText("Public")).not.toBeInTheDocument();
	});

	test("a description draws `backticked` spans as code", () => {
		render(
			<ApiTagView
				title="S"
				page={tagPage([
					operation({ description: "Sets `replicas` on the `web` process." }),
				])}
			/>,
		);
		expect(screen.getByText("replicas").tagName).toBe("CODE");
		expect(screen.getByText("web").tagName).toBe("CODE");
		expect(screen.getByText(/Sets/)).toBeInTheDocument();
	});

	test("parameters are tabulated with where they go, their type and whether they are required", () => {
		render(
			<ApiTagView
				title="S"
				page={tagPage([
					operation({
						params: [
							{
								name: "serviceId",
								in: "path",
								required: true,
								type: "string",
								description: "The service",
							},
							{
								name: "limit",
								in: "query",
								required: false,
								type: "integer",
								description: "Page size",
							},
						],
					}),
				])}
			/>,
		);
		const rows = screen.getAllByRole("row");
		const serviceId = rows.find((row) =>
			within(row).queryByText("serviceId"),
		) as HTMLElement;
		expect(within(serviceId).getByText("required")).toBeInTheDocument();
		expect(within(serviceId).getByText("path")).toBeInTheDocument();
		expect(within(serviceId).getByText("The service")).toBeInTheDocument();
		const limit = rows.find((row) =>
			within(row).queryByText("limit"),
		) as HTMLElement;
		expect(within(limit).queryByText("required")).not.toBeInTheDocument();
		expect(within(limit).getByText("integer")).toBeInTheDocument();
	});

	test("an operation without parameters has no table", () => {
		render(<ApiTagView title="S" page={tagPage([operation()])} />);
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("the request body and successful responses are sections, titled with their content type", () => {
		render(
			<ApiTagView
				title="S"
				page={tagPage([
					operation({
						body: {
							contentType: "application/json",
							required: true,
							schema: { type: "object", props: [] },
						},
						responses: [
							{
								status: "200",
								description: "OK",
								contentType: "application/json",
								schema: { type: "string" },
							},
							{ status: "204", description: "Gone" },
						],
					}),
				])}
			/>,
		);
		expect(
			screen.getByText("Request body · application/json · required"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Response 200 · OK · application/json"),
		).toBeInTheDocument();
		expect(screen.getByText("Response 204 · Gone")).toBeInTheDocument();
		expect(screen.getByText("No body.")).toBeInTheDocument();
	});

	test("an optional body is not marked required", () => {
		render(
			<ApiTagView
				title="S"
				page={tagPage([
					operation({
						body: {
							contentType: "application/json",
							required: false,
							schema: {},
						},
					}),
				])}
			/>,
		);
		expect(
			screen.getByText("Request body · application/json"),
		).toBeInTheDocument();
	});

	test("error responses list their status and the stable codes the description names", () => {
		render(
			<ApiTagView
				title="S"
				page={tagPage([
					operation({
						responses: [
							{ status: "200", description: "OK" },
							{ status: "404", description: "Not Found" },
							{ status: "409", description: "Conflict" },
						],
						errorCodes: [
							{ status: "409", code: "deployment_in_progress" },
							{ status: "409", code: "generation_conflict" },
						],
					}),
				])}
			/>,
		);
		const errors = screen.getByText("Errors").parentElement as HTMLElement;
		const items = within(errors).getAllByRole("listitem");
		expect(items).toHaveLength(2);
		expect(
			within(items[0] as HTMLElement).getByText("404"),
		).toBeInTheDocument();
		expect(
			within(items[0] as HTMLElement).queryByText("deployment_in_progress"),
		).toBeNull();
		expect(
			within(items[1] as HTMLElement).getByText("deployment_in_progress"),
		).toBeInTheDocument();
		expect(
			within(items[1] as HTMLElement).getByText("generation_conflict"),
		).toBeInTheDocument();
		expect(screen.getByText("application/problem+json")).toBeInTheDocument();
	});

	test("an operation with only successful responses has no Errors section", () => {
		render(
			<ApiTagView
				title="S"
				page={tagPage([
					operation({ responses: [{ status: "200", description: "OK" }] }),
				])}
			/>,
		);
		expect(screen.queryByText("Errors")).not.toBeInTheDocument();
	});
});

describe("schema trees", () => {
	const defs: Record<string, ApiSchema> = {
		Service: {
			type: "object",
			description: "A deployable unit",
			props: [
				{
					name: "id",
					required: true,
					schema: { type: "string", description: "Its id" },
				},
				{ name: "owner", required: false, schema: { ref: "Owner" } },
				{
					name: "tags",
					required: false,
					schema: { type: "array", items: { ref: "Tag" } },
				},
				{
					name: "env",
					required: false,
					schema: { type: "object", additional: { type: "string" } },
				},
				{
					name: "mode",
					required: false,
					schema: { variants: [{ type: "string" }, { type: "integer" }] },
				},
			],
		},
		Owner: {
			type: "object",
			props: [{ name: "self", required: false, schema: { ref: "Owner" } }],
		},
		Tag: {
			type: "object",
			props: [{ name: "label", required: true, schema: { type: "string" } }],
		},
	};

	function renderBody(schema: ApiSchema) {
		return render(
			<ApiTagView
				title="S"
				page={tagPage(
					[
						operation({
							responses: [{ status: "200", description: "OK", schema }],
						}),
					],
					defs,
				)}
			/>,
		);
	}

	test("a named schema lists its properties with type, requiredness and description", () => {
		renderBody({ ref: "Service" });
		const id = screen.getByText("id").closest("li") as HTMLElement;
		expect(within(id).getByText("string")).toBeInTheDocument();
		expect(within(id).getByText("required")).toBeInTheDocument();
		expect(within(id).getByText("Its id")).toBeInTheDocument();
		// A property that is a named schema shows its name and the description of the definition.
		const owner = screen
			.getByRole("button", { name: /owner/ })
			.closest("li") as HTMLElement;
		expect(within(owner).getByText("Owner")).toBeInTheDocument();
	});

	test("scalars are plain text; objects, named schemas and arrays of them are expandable", () => {
		renderBody({ ref: "Service" });
		expect(screen.queryByRole("button", { name: /\bid\b/ })).toBeNull();
		expect(screen.getByRole("button", { name: "▸ owner" })).toHaveAttribute(
			"aria-expanded",
			"false",
		);
		expect(screen.getByRole("button", { name: "▸ tags" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "▸ env" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "▸ mode" })).toBeInTheDocument();
	});

	test("expanding a property shows its members and collapsing hides them", async () => {
		renderBody({ ref: "Service" });
		const user = userEvent.setup();
		expect(screen.queryByText("label")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "▸ tags" }));
		expect(screen.getByRole("button", { name: "▾ tags" })).toHaveAttribute(
			"aria-expanded",
			"true",
		);
		expect(screen.getByText("label")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "▾ tags" }));
		expect(screen.queryByText("label")).not.toBeInTheDocument();
	});

	test("a free-form map shows a [key] row and a union shows numbered options", async () => {
		renderBody({ ref: "Service" });
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "▸ env" }));
		expect(screen.getByText("[key]")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "▸ mode" }));
		expect(screen.getByText("option 1")).toBeInTheDocument();
		expect(screen.getByText("option 2")).toBeInTheDocument();
	});

	test("a schema that refers to itself opens one level at a time instead of recursing", async () => {
		renderBody({ ref: "Service" });
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "▸ owner" }));
		const self = screen.getByRole("button", { name: "▸ self" });
		await user.click(self);
		expect(screen.getAllByRole("button", { name: /self/ })).toHaveLength(2);
	});

	test("the label above the tree names the schema", () => {
		renderBody({ type: "array", items: { ref: "Tag" } });
		expect(screen.getByText("Tag[]")).toBeInTheDocument();
		// The array's element schema is what expands.
		expect(screen.getByText("label")).toBeInTheDocument();
	});

	test("a ref no definition holds draws its name and nothing to open", () => {
		renderBody({ ref: "Missing" });
		expect(screen.getByText("Missing")).toBeInTheDocument();
		expect(screen.queryByRole("listitem")).not.toBeInTheDocument();
	});
});
