import { NebulaMark } from "@nebulactrl/dither/images/nebula-mark-g";
import { container, InstallWell } from "./shared.tsx";

/**
 * The closing, and the end of /compare and /faq: one invitation and the install
 * command in a floating plate on the page's floor, with the 1px accent hairline
 * along its top. No texture: the hero's scene is the page's one. The mark above
 * the heading opens once as it scrolls in, and on hover.
 */
export function FinalCta({ command }: { command: string }) {
	return (
		<section className="pt-24 pb-24 md:pt-40 md:pb-32" id="install-again">
			<div className={container}>
				<div className="closing-plate surface-float relative flex flex-col items-center overflow-hidden rounded-2xl px-5 py-14 text-center sm:px-10 md:py-20">
					<span
						aria-hidden
						data-splay-host
						className="mb-7 inline-block size-14 md:size-16"
					>
						<NebulaMark size={96} hover className="size-full" />
					</span>
					<h2 className="display-2 m-0 max-w-[20ch] text-ink-1">
						Try it on a server you already have.
					</h2>
					<p className="m-0 mt-5 max-w-[56ch] text-ink-2 text-lg">
						Free. No sign-up, no licence key. Uninstalling leaves your
						containers running.
					</p>
					<div className="mt-9 w-full max-w-[720px] text-left">
						<InstallWell command={command} id="install-final" />
						<p className="m-0 mt-6 flex flex-wrap justify-center gap-3 text-sm">
							<a
								className="btn-secondary pressable inline-flex h-10 items-center rounded-lg px-4 font-medium no-underline"
								href="/faq"
							>
								FAQ
							</a>
							<a
								className="btn-secondary pressable inline-flex h-10 items-center rounded-lg px-4 font-medium no-underline"
								href="/compare"
							>
								How it compares
							</a>
						</p>
					</div>
				</div>
			</div>
		</section>
	);
}
