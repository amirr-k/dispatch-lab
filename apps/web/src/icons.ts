import { Car, House, Package, TrafficCone } from "lucide-react";

// One place deciding what each thing on the map looks like, for the same
// reason colors.ts exists: the legend and the map have to agree, and they
// can't if each picks its own glyph.
export const ICONS = {
  driver: Car,
  pickup: Package,
  destination: House,
  closure: TrafficCone,
};

export type MapIcon = (typeof ICONS)[keyof typeof ICONS];
