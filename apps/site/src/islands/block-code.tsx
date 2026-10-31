import { BlockCode } from "@nebulactrl/dither/interface/block-code";

/** The 404 code in block letters: one pixel per dither cell, printed in once, then still with a short glitch every few seconds. Clean and static under reduced motion. */
export default function SiteBlockCode({ code }: { code: string }) {
	return <BlockCode code={code} cell={4} depth={5} motion="glitch" />;
}
