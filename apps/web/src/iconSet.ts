import { withBase } from "./basePath";

// One place deciding what each thing on the map looks like, for the same
// reason colors.ts exists: the legend and the map have to agree, and they
// can't if each picks its own glyph.
// These are the cartoon images provided for the UI: a truck, a paper bag, and
// a house, plus an SVG hazard symbol for closed roads. They live in public/,
// so unlike an imported asset they need withBase themselves - Vite only
// rewrites paths it sees at build time (imports, index.html), not string
// literals a plain object holds.
export const ICONS = {
  driver: withBase("/driver.png"),
  pickup: withBase("/package.png"),
  destination: withBase("/house.png"),
  closure: "hazard",
};

export type MapIcon = (typeof ICONS)[keyof typeof ICONS];
