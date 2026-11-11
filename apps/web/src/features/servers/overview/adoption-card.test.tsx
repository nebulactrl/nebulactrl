import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	closeAdoption,
	useAdoptionState,
} from "#/features/server-adoption/store";
import { renderWithProviders } from "#/test/render";
import { toServerView } from "../model";
import { serverWire } from "../server.fixture";
import { AdoptionCard } from "./adoption-card";

afterEach(() => act(() => closeAdoption()));

const view = (adoption?: {
	state: "held" | "";
	found: number;
	unmanaged: number;
}) =>
	toServerView(serverWire({ id: "srv_1", ...(adoption ? { adoption } : {}) }));

describe("AdoptionCard", () => {
	test("renders nothing for a server with no adoption", async () => {
		const { container } = await renderWithProviders(
			<AdoptionCard server={view()} />,
		);
		expect(container).toBeEmptyDOMElement();
	});

	test("renders nothing once reviewed with nothing left unmanaged", async () => {
		const { container } = await renderWithProviders(
			<AdoptionCard server={view({ state: "", found: 0, unmanaged: 0 })} />,
		);
		expect(container).toBeEmptyDOMElement();
	});

	test("held: says nothing is changed until the review, and opens it", async () => {
		const adoption = renderHook(() => useAdoptionState());
		const user = userEvent.setup();
		await renderWithProviders(
			<AdoptionCard server={view({ state: "held", found: 1, unmanaged: 0 })} />,
		);
		expect(
			screen.getByText(
				"This host runs 1 service from an earlier NebulaCtrl. Nothing on it is changed until you review them.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Review workloads" }));
		expect(adoption.result.current).toEqual({ open: true, serverId: "srv_1" });
	});

	test("unmanaged workloads are counted, with a Review button", async () => {
		const adoption = renderHook(() => useAdoptionState());
		const user = userEvent.setup();
		await renderWithProviders(
			<AdoptionCard server={view({ state: "", found: 0, unmanaged: 3 })} />,
		);
		expect(
			screen.getByText(/3 workloads run here unmanaged/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Review" }));
		expect(adoption.result.current).toEqual({ open: true, serverId: "srv_1" });
	});

	test("one unmanaged workload is not pluralised", async () => {
		await renderWithProviders(
			<AdoptionCard server={view({ state: "", found: 0, unmanaged: 1 })} />,
		);
		expect(
			screen.getByText(/1 workload runs here unmanaged/),
		).toBeInTheDocument();
	});
});
