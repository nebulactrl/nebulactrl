import { createEclipse } from "@nebulactrl/ascii/eclipse";
import { AsciiScene } from "./ascii-scene.tsx";

/**
 * The Eclipse behind the landing hero: the page's one scene, a pixel-dithered
 * ringed planet whose light follows the cursor.
 * The hero's copy column is the panel it dims around (`data-ascii-panel`) and
 * its quiet region (cleared to the page background), so the planet takes the
 * free side of the window; on a narrow window it sits behind the copy, dimmed.
 * The halftone floor (`ht-hero`) is the fallback and stands down once a frame
 * is painted.
 */
export default function HeroEclipse() {
	return <AsciiScene scene="eclipse" create={createEclipse} />;
}
