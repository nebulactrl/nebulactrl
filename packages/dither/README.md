# @nebulactrl/dither

NebulaCtrl's ordered-dither kit, shared by `apps/web` and `apps/site`: the engine in `src/lib/**` and the chart, background, image, interface and text components in `src/components/{charts,backgrounds,images,interface,text}/**`. TypeScript sources are consumed directly (Vite, bun), there is no build step.

Read [`src/lib/README.md`](src/lib/README.md) first. Entry points are listed in `package.json` `exports` (`@nebulactrl/dither/lib`, `/lib/gl`, `/charts`, `/backgrounds`, `/images`, `/interface`, `/text`, plus deep entries per file).

The interface pieces that wrap a primitive (`DitherButton`, `DitherCard`, `DitherAvatar`, `DitherEmptyState`, `DitherNotFound`), `DitherStatCard`, `DitherNumber`, `DitheredLogo` and `LogoMarquee` are part of the package. They sit on Base UI and the design vocabulary classes both apps define (`btn-*`, `pressable`, `surface-*`, `text-ink-*`), and take what the host owns through small hooks: brand logos through `setBrandCatalog`. The dev galleries (`/dev/dither`) stay in `apps/web`.

Tokens (`--dither-*`, and the motion scale the components animate with) live in `packages/tokens` (`@nebulactrl/tokens`): import `@nebulactrl/tokens/dither.css` and `@nebulactrl/tokens/motion.css` in the app's stylesheet.

```sh
bun test src
bunx tsc --noEmit -p .
```
