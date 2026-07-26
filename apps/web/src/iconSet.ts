// One place deciding what each thing on the map looks like, for the same
// reason colors.ts exists: the legend and the map have to agree, and they
// can't if each picks its own glyph.
// These are the cartoon images provided for the UI: a truck, a paper bag, and
// a house, plus an SVG hazard symbol for closed roads.
export const ICONS = {
  driver: "/driver.png",
  pickup: "/package.png",
  destination: "/house.png",
  closure: "hazard",
};

export type MapIcon = (typeof ICONS)[keyof typeof ICONS];
