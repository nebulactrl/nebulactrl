import { mock } from "bun:test";
import type { DitherShaderProps } from "../../lib/gl";
import * as realGl from "../../lib/gl";
import * as realMotion from "../../lib/motion";

/**
 * Test helper for the backgrounds: swaps the GPU shader for a stand-in that
 * records the props it was given, so a test can read what a background hands
 * the engine (palette, matrix, uniforms, cell, fps, animate...) instead of the
 * pixels. Without WebGL the real shader only ever shows its fallback.
 *
 * Call `installProbe()` before importing the component under test (with a
 * cache-busting query) and `restoreProbe()` in `afterAll`: bun test modules are
 * shared by every file in one run.
 */

type FrameFn = (
	time: number,
	delta: number,
	still?: "reduced" | "disabled",
) => void;

type FrameOptions = Parameters<typeof realMotion.useDitherFrame>[1];

/** The newest `useDitherFrame` registration, so a test can play frames by hand. */
export const frame: {
	run: FrameFn | null;
	options: FrameOptions;
	registrations: number;
} = { run: null, options: undefined, registrations: 0 };

/** What `prefersReducedMotion()` answers while the probe is installed. */
export const motion = { reduced: false };

/** Every `DitherShader` render, oldest first. */
export const shaderCalls: DitherShaderProps[] = [];

/** The props of the newest shader render. */
export function lastShader(): DitherShaderProps {
	const call = shaderCalls.at(-1);
	if (!call) throw new Error("no shader was rendered");
	return call;
}

/** The newest shader's uniforms (they must exist). */
export function lastUniforms(): NonNullable<DitherShaderProps["uniforms"]> {
	const uniforms = lastShader().uniforms;
	if (!uniforms) throw new Error("the shader got no uniforms");
	return uniforms;
}

export function resetProbe(): void {
	shaderCalls.length = 0;
	frame.run = null;
	frame.options = undefined;
	frame.registrations = 0;
	motion.reduced = false;
}

function ProbeShader(props: DitherShaderProps) {
	shaderCalls.push(props);
	return (
		<div
			data-slot="probe-shader"
			aria-hidden={props["aria-hidden"] === false ? undefined : true}
			className={props.className}
		/>
	);
}

const real = { ...realGl };
const realMotionExports = { ...realMotion };

export function installProbe(): void {
	mock.module("../../lib/gl", () => ({ ...real, DitherShader: ProbeShader }));
	mock.module("../../lib/motion", () => ({
		...realMotionExports,
		prefersReducedMotion: () => motion.reduced,
		useDitherFrame: (run: FrameFn, options: FrameOptions) => {
			frame.run = run;
			frame.options = options;
			frame.registrations += 1;
			return "running";
		},
	}));
}

export function restoreProbe(): void {
	mock.module("../../lib/gl", () => real);
	mock.module("../../lib/motion", () => realMotionExports);
}
