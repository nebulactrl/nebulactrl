import { useRouterState, useSearch } from "@tanstack/react-router";
import { type ReactNode, useEffect, useRef, useSyncExternalStore } from "react";
import {
	AnimatedToastStack,
	createAnimatedToastStore,
	type ToastActionConfig,
	type ToastKind,
} from "#/components/motion/animated-toast-stack";

interface ToastActionProps {
	children: ReactNode;
	onClick: () => void;
}

interface ToastOptions {
	id?: string;
	title: ReactNode;
	description?: ReactNode;
	/** @default "info" */
	type?: ToastKind;
	/** `check` draws a circle-check in place of the success lamp; see `toast.deployed`. */
	glyph?: "check";
	/** Milliseconds before auto-dismiss; 0 never dismisses on its own.
	 * Errors default to 0 — a failure the reader missed is worse than a
	 * card that lingers until they dismiss it. */
	timeout?: number;
	/** The outline action: "View". */
	actionProps?: ToastActionProps;
	/** The solid, primary action: "Undo". */
	secondaryActionProps?: ToastActionProps;
	dismissible?: boolean;
}

function toAction(
	props: ToastActionProps | undefined,
): ToastActionConfig | undefined {
	if (!props) return undefined;
	return {
		label: props.children,
		onClick: props.onClick,
	};
}

const store = createAnimatedToastStore();

const toastApi = {
	add(options: ToastOptions): string {
		return store.add({
			id: options.id,
			title: options.title,
			description: options.description,
			kind: options.type ?? "info",
			glyph: options.glyph,
			duration: options.timeout ?? (options.type === "error" ? 0 : undefined),
			dismissible: options.dismissible,
			action: toAction(options.actionProps),
			primaryAction: toAction(options.secondaryActionProps),
		});
	},
	/** In-place progress: a "Restarting…" loading toast becomes "Restarted". */
	update(id: string, patch: Partial<ToastOptions>): void {
		store.update(id, {
			title: patch.title,
			description: patch.description,
			kind: patch.type,
			glyph: patch.glyph,
			duration:
				patch.timeout ??
				(patch.type === "error" ? 0 : patch.type ? 4200 : undefined),
			dismissible: patch.dismissible,
			action: patch.actionProps ? toAction(patch.actionProps) : undefined,
			primaryAction: patch.secondaryActionProps
				? toAction(patch.secondaryActionProps)
				: undefined,
		});
	},
	close(id?: string): void {
		if (id) store.dismiss(id);
		else store.clear();
	},
};

/**
 * `toast({ title, description, type })` raises a toast and returns its id;
 * `toast.add`, `toast.update` and `toast.close` are the same store, and
 * `toast.success|error|info|warning(title, description?)` are shorthands.
 */
const toast = Object.assign(
	(options: ToastOptions): string => toastApi.add(options),
	toastApi,
	{
		/** A success for "your deploy was sent": the circle-check draws its tick. */
		deployed: (title: ReactNode, description?: ReactNode) =>
			toastApi.add({
				title,
				type: "success",
				glyph: "check",
				...(description === undefined ? {} : { description }),
			}),
		success: (title: ReactNode, description?: ReactNode) =>
			toastApi.add({
				title,
				type: "success",
				...(description === undefined ? {} : { description }),
			}),
		error: (title: ReactNode, description?: ReactNode) =>
			toastApi.add({
				title,
				type: "error",
				...(description === undefined ? {} : { description }),
			}),
		info: (title: ReactNode, description?: ReactNode) =>
			toastApi.add({
				title,
				type: "info",
				...(description === undefined ? {} : { description }),
			}),
		warning: (title: ReactNode, description?: ReactNode) =>
			toastApi.add({
				title,
				type: "warning",
				...(description === undefined ? {} : { description }),
			}),
	},
);

// The canvas minimap sits in the bottom-right corner a toast would
// otherwise cover; a canvas with no inspector open raises the toast clear
// of it, so callers never need to know. A narrow screen (under 760px)
// hides the minimap and drops back to the default.
const NARROW = "(max-width: 759px)";

function subscribeNarrow(onChange: () => void) {
	const query = window.matchMedia(NARROW);
	query.addEventListener("change", onChange);
	return () => query.removeEventListener("change", onChange);
}

function useToastClearance(): boolean {
	const segments = useRouterState({
		select: (state) => state.location.pathname.split("/"),
	});
	const search = useSearch({ strict: false }) as {
		service?: string;
		panel?: string;
	};
	const narrow = useSyncExternalStore(
		subscribeNarrow,
		() => window.matchMedia(NARROW).matches,
		() => false,
	);
	const onOpenCanvas =
		segments[1] === "p" &&
		segments.length === 4 &&
		!search.service &&
		!search.panel &&
		!narrow;
	return onOpenCanvas;
}

// A toast is a receipt for something on the screen it was raised on; it
// stops being one once the reader leaves that section (the first path
// segment: projects home, `p` for a canvas, servers, settings), so a
// switch there clears whatever is still showing.
function useDismissToastsOnSectionChange(): void {
	const section = useRouterState({
		select: (state) => state.location.pathname.split("/")[1],
	});
	const previous = useRef(section);
	useEffect(() => {
		if (previous.current !== section) {
			previous.current = section;
			toast.close();
		}
	}, [section]);
}

/** Mounted once around the app; renders the toast stack, raised
 * clear of the minimap or the Approve bar when either is on screen. */
function Toaster({ children }: { children: ReactNode }) {
	useDismissToastsOnSectionChange();
	const toasts = useSyncExternalStore(store.subscribe, store.getSnapshot);
	const raised = useToastClearance();

	return (
		<>
			{children}
			<AnimatedToastStack
				toasts={toasts}
				onDismiss={(id) => store.dismiss(id)}
				onPause={store.pause}
				onResume={store.resume}
				bottom={raised ? 150 : 20}
			/>
		</>
	);
}

export { Toaster, toast };
