import { ICONS } from "./iconSet";

export type MapIcon = (typeof ICONS)[keyof typeof ICONS];

// Glyph renders one of the cartoon PNG images at a point in the parent SVG's
// own coordinate space. The images are displayed inside a square whose center
// is (x, y), so they sit naturally alongside the roads.
export function Glyph({ icon, x, y, size, color }: {
  icon: MapIcon;
  x: number;
  y: number;
  size: number;
  color: string;
}) {
  if (icon === "hazard") {
    // closed roads draw their own hazard sign, so this path is only here for
    // completeness.
    return (
      <polygon
        points={`${x},${y - size / 2} ${x - size * 0.45},${y + size * 0.35} ${x + size * 0.45},${y + size * 0.35}`}
        fill="none"
        stroke={color}
        strokeWidth={2}
      />
    );
  }

  return (
    <image
      href={icon}
      x={x - size / 2}
      y={y - size / 2}
      width={size}
      height={size}
      preserveAspectRatio="xMidYMid meet"
    />
  );
}
