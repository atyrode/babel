import type { CSSProperties, ReactNode } from "react";

// Inline SVG charts, written here rather than imported.
//
// The content security policy this surface serves under forbids external
// assets, so a charting library is not merely heavy here — it is unloadable.
// That constraint turns out to fit what these charts have to say: every one of
// them draws a bounded daily series where the shape is the message and the
// exact figure is read from the `.stat` beside it, which is three primitives
// rather than a library.
//
// One rule runs through all three: an unknown is a gap, never a zero. A day
// whose spend was not recorded is drawn as absent, because a zero-height bar
// among real ones reads as "nothing was spent" — a claim about money that
// nobody made. The callers pass `null` for those days and the geometry skips
// them; hover text says so in words.
//
// Colour arrives as a CSS custom-property reference (`var(--accent)`) rather
// than a literal, so the palette stays in the design tokens and a chart cannot
// invent a colour of its own.

// The drawing box every chart shares. The SVG scales to its host box with
// preserveAspectRatio="none", so these units are a coordinate space and not
// pixels: a caller changes the rendered height by setting --spark-height on the
// .spark host, never by rewriting the geometry.
const W = 100;
const H = 24;

// A hairline still visible after the non-uniform scale. Bars use a minimum
// height so that a day with one record is distinguishable from a day with
// none, which is the difference the reader is scanning for.
const MIN_BAR = 0.6;

function SparkHost({
  height,
  block,
  label,
  children,
}: {
  height?: string;
  block?: boolean;
  label: string;
  children: ReactNode;
}) {
  // --spark-height is DesignSystem's own hook on .spark; a chart sets it
  // rather than styling the SVG, so the host keeps deciding the box.
  const style = height ? ({ "--spark-height": height } as CSSProperties) : undefined;
  return (
    <span className={block ? "spark block" : "spark"} style={style}>
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-label={label}>
        {children}
      </svg>
    </span>
  );
}

export type Point = number | null | undefined;

function ceiling(values: readonly Point[]): number {
  let max = 0;
  for (const value of values) {
    if (typeof value === "number" && Number.isFinite(value) && value > max) max = value;
  }
  return max;
}

// Sparkline draws one series as a line, broken wherever the series has no
// value. The break is the point: a line interpolated across a day nobody
// measured would draw a trend out of an absence.
export function Sparkline({
  values,
  label,
  color = "currentColor",
  height,
  block,
  area = true,
}: {
  values: readonly Point[];
  label: string;
  color?: string;
  height?: string;
  block?: boolean;
  area?: boolean;
}) {
  const max = ceiling(values);
  // The vertical map keeps a half-unit of headroom so the peak's stroke is not
  // clipped by the viewBox, and pins a flat all-zero series to the baseline
  // rather than to the middle of an empty box.
  const span = values.length > 1 ? values.length - 1 : 1;
  const y = (value: number) => (max === 0 ? H - MIN_BAR : H - (value / max) * (H - 1) - 0.5);

  // Runs of consecutive known values. Each becomes its own polyline, so a gap
  // costs a segment rather than a straight line through it.
  const runs: Array<Array<[number, number]>> = [];
  let run: Array<[number, number]> = [];
  values.forEach((value, index) => {
    if (typeof value !== "number" || !Number.isFinite(value)) {
      if (run.length) runs.push(run);
      run = [];
      return;
    }
    run.push([(index / span) * W, y(value)]);
  });
  if (run.length) runs.push(run);

  return (
    <SparkHost height={height} block={block} label={label}>
      {area &&
        runs.map((segment, index) => {
          if (segment.length < 2) return null;
          const path =
            `M${segment[0][0].toFixed(2)},${H} ` +
            segment.map(([px, py]) => `L${px.toFixed(2)},${py.toFixed(2)}`).join(" ") +
            ` L${segment[segment.length - 1][0].toFixed(2)},${H} Z`;
          return <path key={`fill-${index}`} d={path} fill={color} fillOpacity={0.16} stroke="none" />;
        })}
      {runs.map((segment, index) =>
        segment.length === 1 ? (
          <circle
            key={`dot-${index}`}
            cx={segment[0][0]}
            cy={segment[0][1]}
            r={0.9}
            fill={color}
            stroke="none"
          />
        ) : (
          <polyline
            key={`line-${index}`}
            points={segment.map(([px, py]) => `${px.toFixed(2)},${py.toFixed(2)}`).join(" ")}
            fill="none"
            stroke={color}
            strokeWidth={1}
            vectorEffect="non-scaling-stroke"
            strokeLinejoin="round"
          />
        ),
      )}
    </SparkHost>
  );
}

// One coloured component of a stacked column, in the order it stacks.
export interface Band {
  key: string;
  label: string;
  color: string;
  values: readonly Point[];
}

// StackedBars draws one column per day, each column stacked by band. It is the
// records-per-day chart: four kinds whose sum is the day's output and whose
// composition is the thing worth seeing, which a line chart cannot show and
// four separate charts make the reader add up by eye.
//
// `titles` is the hover text per column, supplied by the caller because only
// the caller knows how to word the day's own figures.
export function StackedBars({
  bands,
  titles,
  label,
  height,
  block = true,
}: {
  bands: readonly Band[];
  titles: readonly string[];
  label: string;
  height?: string;
  block?: boolean;
}) {
  const columns = bands.reduce((most, band) => Math.max(most, band.values.length), 0);
  const totals: number[] = [];
  for (let index = 0; index < columns; index += 1) {
    let sum = 0;
    for (const band of bands) {
      const value = band.values[index];
      if (typeof value === "number" && Number.isFinite(value)) sum += value;
    }
    totals.push(sum);
  }
  const max = ceiling(totals);
  const slot = columns > 0 ? W / columns : W;
  const gap = slot > 2 ? Math.min(0.8, slot * 0.18) : 0;
  const width = Math.max(slot - gap, 0.4);

  return (
    <SparkHost height={height} block={block} label={label}>
      {totals.map((total, index) => {
        const left = index * slot + gap / 2;
        if (max === 0 || total === 0) {
          return (
            <g key={index}>
              <title>{titles[index] ?? ""}</title>
              <rect x={left} y={H - MIN_BAR} width={width} height={MIN_BAR} fill="currentColor" fillOpacity={0.22} />
            </g>
          );
        }
        let top = H;
        return (
          <g key={index}>
            <title>{titles[index] ?? ""}</title>
            {bands.map((band) => {
              const value = band.values[index];
              if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return null;
              const tall = Math.max((value / max) * H, MIN_BAR);
              top -= tall;
              return <rect key={band.key} x={left} y={top} width={width} height={tall} fill={band.color} />;
            })}
          </g>
        );
      })}
    </SparkHost>
  );
}

// Bars draws one series as columns, with an unmeasured day drawn as a faint
// baseline tick rather than a column of zero height. The tick is deliberately
// visible: the reader has to be able to see that the day exists and that its
// figure does not.
export function Bars({
  values,
  titles,
  label,
  color = "currentColor",
  height,
  block = true,
}: {
  values: readonly Point[];
  titles: readonly string[];
  label: string;
  color?: string;
  height?: string;
  block?: boolean;
}) {
  const max = ceiling(values);
  const slot = values.length > 0 ? W / values.length : W;
  const gap = slot > 2 ? Math.min(0.8, slot * 0.18) : 0;
  const width = Math.max(slot - gap, 0.4);

  return (
    <SparkHost height={height} block={block} label={label}>
      {values.map((value, index) => {
        const left = index * slot + gap / 2;
        const known = typeof value === "number" && Number.isFinite(value) ? value : null;
        // A known zero and an unknown both draw the baseline tick; the fill
        // opacity is what distinguishes them, and the hover title says which.
        const tall = known !== null && max > 0 ? Math.max((known / max) * H, MIN_BAR) : MIN_BAR;
        return (
          <g key={index}>
            <title>{titles[index] ?? ""}</title>
            <rect
              x={left}
              y={H - tall}
              width={width}
              height={tall}
              fill={known !== null ? color : "currentColor"}
              fillOpacity={known !== null ? 1 : 0.18}
            />
          </g>
        );
      })}
    </SparkHost>
  );
}
