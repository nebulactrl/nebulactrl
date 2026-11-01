import { createEclipse } from "@nebulactrl/ascii/eclipse";
import { AsciiScene } from "./ascii-scene.tsx";

/**
 * The Eclipse behind the landing hero: the page's one scene.
 * The hero's copy column is the panel it dims around (`data-ascii-panel`), so
 * the orb takes the free side of the window; on a narrow window it sits behind
 * the copy, dimmed. The halftone floor (`ht-hero`) is the fallback and stands
 * down once a frame is painted.
 */
export default function HeroEclipse() {
	return <AsciiScene scene="eclipse" create={createEclipse} />;
}
