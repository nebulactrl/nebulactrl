import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, waitFor } from "@testing-library/react";
import { useRef } from "react";
import { stubCanvas2d } from "../canvas-stub";
import { DitherLayer, type DitherLayerProps } from "./dither-layer";
import { installImageEnv } from "./test-env";

let env: Awaited<ReturnType<typeof installImageEnv>>;
beforeEach(async () => {
	env = await installImageEnv(stubCanvas2d);
});
afterEach(() => env.restore());

let n = 0;
const src = (name: string) => `/layer-${name}-${++n}.png`;

type State = Parameters<NonNullable<DitherLayerProps["onState"]>>[0];

function Host(props: Partial<DitherLayerProps> & { src: string }) {
	const boxRef = useRef<HTMLDivElement>(null);
	return (
		<div ref={boxRef} data-testid="box">
			<DitherLayer
				look={{ cell: 4, palette: ["#000000", "#ffffff"] }}
				boxRef={boxRef}
				{...props}
			/>
		</div>
	);
}

const canvasOf = (root: HTMLElement) => root.querySelector("canvas");

describe("DitherLayer", () => {
	test("draws nothing until the box is measured and the image is dithered", async () => {
		const view = render(<Host src={src("a")} />);
		await act(async () => {});
		expect(canvasOf(view.container)).toBeNull();
		env.observers.resize(view.getByTestId("box"), 200, 100);
		await waitFor(() => expect(canvasOf(view.container)).not.toBeNull());
	});

	test("the canvas covers the box's grid in whole CSS px, crisp and out of the flow", async () => {
		const view = render(<Host src={src("b")} />);
		env.observers.resize(view.getByTestId("box"), 202, 101);
		await waitFor(() => expect(canvasOf(view.container)).not.toBeNull());
		const canvas = canvasOf(view.container) as HTMLCanvasElement;
		// 4px cells: 51 columns × 26 rows cover 202×101.
		expect(canvas.width).toBe(51);
		expect(canvas.height).toBe(26);
		expect(canvas.style.width).toBe("204px");
		expect(canvas.style.height).toBe("104px");
		expect(canvas.style.position).toBe("absolute");
		expect(canvas.style.imageRendering).toBe("pixelated");
	});

	test("the dithered pixels are put on the canvas", async () => {
		const view = render(<Host src={src("c")} />);
		env.observers.resize(view.getByTestId("box"), 40, 20);
		await waitFor(() => expect(canvasOf(view.container)).not.toBeNull());
		const canvas = canvasOf(view.container) as HTMLCanvasElement;
		const put = env.canvas.contextOf(canvas)?.puts.at(-1);
		expect(put).toMatchObject({ width: 10, height: 5 });
		const colours = new Set<string>();
		for (let i = 0; i < (put?.data.length ?? 0); i += 4) {
			colours.add(`${put?.data[i]},${put?.data[i + 1]},${put?.data[i + 2]}`);
		}
		for (const colour of colours)
			expect(["0,0,0", "255,255,255"]).toContain(colour);
	});

	test("className and style land on the canvas; style wins over the layout", async () => {
		const view = render(
			<Host
				src={src("d")}
				className="opacity-50"
				style={{ left: 5, imageRendering: "auto" }}
			/>,
		);
		env.observers.resize(view.getByTestId("box"), 40, 20);
		await waitFor(() => expect(canvasOf(view.container)).not.toBeNull());
		const canvas = canvasOf(view.container) as HTMLCanvasElement;
		expect(canvas).toHaveClass("opacity-50");
		expect(canvas.style.left).toBe("5px");
		expect(canvas.style.imageRendering).toBe("auto");
	});

	test("reports loading and then ready with the frame count and the image's natural size", async () => {
		const states: State[] = [];
		const view = render(
			<Host
				src={src("e")}
				onState={(s) => states.push(s)}
				multipliers={[2, 1]}
			/>,
		);
		expect(states[0]).toEqual({ status: "idle", natural: null, count: 0 });
		env.observers.resize(view.getByTestId("box"), 80, 40);
		await waitFor(() => expect(states.at(-1)?.status).toBe("ready"));
		expect(states.map((s) => s.status)).toContain("loading");
		expect(states.at(-1)).toEqual({
			status: "ready",
			natural: { width: 400, height: 300 },
			count: 2,
		});
	});

	test("stage picks which multiplier's frame is shown; past the end it is the finest", async () => {
		const widthAt = async (stage: number | undefined) => {
			const view = render(
				<Host
					src={src("stage")}
					multipliers={[4, 2, 1]}
					{...(stage === undefined ? {} : { stage })}
				/>,
			);
			env.observers.resize(view.getByTestId("box"), 80, 40);
			await waitFor(() => expect(canvasOf(view.container)).not.toBeNull());
			const width = (canvasOf(view.container) as HTMLCanvasElement).width;
			view.unmount();
			return width;
		};
		expect(await widthAt(0)).toBe(5);
		expect(await widthAt(1)).toBe(10);
		expect(await widthAt(2)).toBe(20);
		expect(await widthAt(99)).toBe(20);
		expect(await widthAt(undefined)).toBe(20);
	});

	test("changing the stage repaints the same canvas with another frame", async () => {
		const s = src("repaint");
		const view = render(<Host src={s} multipliers={[2, 1]} stage={0} />);
		env.observers.resize(view.getByTestId("box"), 80, 40);
		await waitFor(() => expect(canvasOf(view.container)).not.toBeNull());
		const canvas = canvasOf(view.container) as HTMLCanvasElement;
		expect(canvas.width).toBe(10);
		view.rerender(<Host src={s} multipliers={[2, 1]} stage={1} />);
		expect(canvasOf(view.container)).toBe(canvas);
		expect(canvas.width).toBe(20);
	});

	test("hands the canvas to onCanvas when it exists and null when it goes", async () => {
		const seen: Array<HTMLCanvasElement | null> = [];
		const onCanvas = (c: HTMLCanvasElement | null) => seen.push(c);
		const view = render(<Host src={src("export")} onCanvas={onCanvas} />);
		expect(seen.at(-1) ?? null).toBeNull();
		env.observers.resize(view.getByTestId("box"), 40, 20);
		await waitFor(() => expect(seen.at(-1)).toBeInstanceOf(HTMLCanvasElement));
		expect(seen.at(-1)).toBe(canvasOf(view.container));
		view.unmount();
		expect(seen.at(-1)).toBeNull();
	});

	test("enabled=false loads nothing", async () => {
		const s = src("disabled");
		const view = render(<Host src={s} enabled={false} />);
		env.observers.resize(view.getByTestId("box"), 80, 40);
		await act(async () => {});
		expect(env.images.loaded).not.toContain(s);
		expect(canvasOf(view.container)).toBeNull();
	});

	test("crossOrigin makes a separate image request for the same src", async () => {
		const shared = src("cors");
		const view = render(
			<>
				<Host src={shared} />
				<Host src={shared} crossOrigin="anonymous" />
			</>,
		);
		for (const box of view.getAllByTestId("box"))
			env.observers.resize(box, 40, 20);
		await waitFor(() =>
			expect(view.container.querySelectorAll("canvas")).toHaveLength(2),
		);
		expect(env.images.loaded.filter((s) => s === shared)).toHaveLength(2);
	});

	test("a broken image reports the error and draws nothing", async () => {
		env.images.restore();
		env.images = (await import("./test-env")).stubImages({ fail: () => true });
		const states: State[] = [];
		const view = render(
			<Host src={src("broken")} onState={(s) => states.push(s)} />,
		);
		env.observers.resize(view.getByTestId("box"), 40, 20);
		await waitFor(() => expect(states.at(-1)?.status).toBe("error"));
		expect(canvasOf(view.container)).toBeNull();
	});
});
