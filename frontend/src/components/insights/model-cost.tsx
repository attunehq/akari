import { formatCost, formatCount, formatTokens } from "../../format";
import type { ModelCost, Trends } from "../../types";
import { Stat, StatStrip } from "../stat-strip";
import { fmtInt, fmtK, modelStyle } from "./format";
import { Legend } from "./legend";
import {
  AxisBaseline,
  AxisTicksY,
  ChartSvg,
  ClipRect,
  scaleLog,
  TooltipRow,
  TooltipTitle,
  useClipId,
} from "./primitives";
import { useChartTooltip } from "./tooltip";

const W = 1000;
const H = 380;
const PL = 52;
const PR = 110;
const PT = 16;
const PB = 30;

const TOKEN_TICKS = [1, 10, 100, 1_000, 10_000, 100_000, 1_000_000, 10_000_000];
const COST_TICKS = [0.001, 0.01, 0.1, 1, 10, 100, 1_000, 10_000, 100_000];
const LABEL_CAP = 12;

function ticksIn(domain: readonly [number, number], candidates: number[]) {
  const [lo, hi] = domain;
  return candidates.filter((v) => v >= lo && v <= hi);
}

function fmtCostTick(v: number): string {
  if (v < 1) return `$${v.toFixed(v < 0.01 ? 3 : 2)}`;
  return `$${fmtK(v)}`;
}

function perMTok(cost: number, tokens: number): string {
  if (tokens <= 0) return "n/a";
  return `${formatCost((cost / tokens) * 1_000_000)}/MTok`;
}

function ModelCostChart({ models }: { models: ModelCost[] }) {
  const clipId = useClipId();
  const { show, hide } = useChartTooltip();
  const { colorOf, labelOf } = modelStyle(models);

  const tokenVals = models.map((m) => m.Tokens).filter((t) => t > 0);
  const costVals = models.map((m) => m.CostUSD).filter((c) => c > 0);
  const xLo = tokenVals.length
    ? Math.max(1, Math.min(...tokenVals) / 1.6)
    : 100;
  const xHi = tokenVals.length
    ? Math.max(1_000, Math.max(...tokenVals) * 2.2)
    : 1_000;
  const yLo = costVals.length
    ? Math.max(0.001, Math.min(...costVals) / 1.6)
    : 0.01;
  const yHi = costVals.length ? Math.max(1, Math.max(...costVals) * 2.2) : 1;
  const xScale = scaleLog([xLo, xHi], [PL, W - PR]);
  const yScale = scaleLog([yLo, yHi], [H - PB, PT]);
  const xTicks = ticksIn([xLo, xHi], TOKEN_TICKS);
  const yTicks = ticksIn([yLo, yHi], COST_TICKS);

  const maxSessions =
    models.reduce((m, row) => Math.max(m, row.Sessions), 0) || 1;
  const rScale = (sessions: number) =>
    4 + Math.sqrt(sessions / maxSessions) * 14;

  let totalCost = 0;
  let totalTokens = 0;
  for (const m of models) {
    totalCost += m.CostUSD;
    totalTokens += m.Tokens;
  }
  const rate = totalTokens > 0 ? totalCost / totalTokens : 0;

  const labeled = new Set(models.slice(0, LABEL_CAP).map((m) => m.Model));

  return (
    <ChartSvg w={W} h={H}>
      <AxisTicksY
        values={yTicks}
        xLeft={PL}
        xRight={W - PR}
        yScale={yScale}
        fmt={fmtCostTick}
      />
      {xTicks.map((v) => (
        <g key={v}>
          <line
            x1={xScale(v)}
            x2={xScale(v)}
            y1={PT}
            y2={H - PB}
            className="gridline"
          />
          <text
            x={xScale(v)}
            y={H - PB + 15}
            className="axis-tick-text"
            textAnchor="middle"
          >
            {formatTokens(v)}
          </text>
        </g>
      ))}
      <AxisBaseline x1={PL} x2={W - PR} y={H - PB} />
      <ClipRect id={clipId} x={PL} y={PT} w={W - PL - PR} h={H - PT - PB}>
        {rate > 0 && (
          <line
            x1={xScale(xLo)}
            y1={yScale(rate * xLo)}
            x2={xScale(xHi)}
            y2={yScale(rate * xHi)}
            stroke="var(--faint)"
            strokeWidth={1.4}
            strokeDasharray="5,4"
          />
        )}
        {models.map((m) => {
          const cx = xScale(Math.max(m.Tokens, xLo));
          const cy = yScale(Math.max(m.CostUSD, yLo));
          const r = rScale(m.Sessions);
          return (
            // biome-ignore lint/a11y/noStaticElementInteractions: mouse-only hover tooltip on a scatter dot; the same figures are already in the stat tiles and Legend below.
            <circle
              key={m.Model}
              cx={cx}
              cy={cy}
              r={r}
              fill={colorOf(m.Model)}
              opacity={0.82}
              stroke="var(--bg)"
              strokeWidth={1}
              className="scatter-dot"
              onMouseMove={(e) =>
                show(
                  e.clientX,
                  e.clientY,
                  <>
                    <TooltipTitle>{m.Model}</TooltipTitle>
                    <TooltipRow>
                      cost <b>{formatCost(m.CostUSD)}</b>
                    </TooltipRow>
                    <TooltipRow>
                      tokens <b>{formatTokens(m.Tokens)}</b>
                    </TooltipRow>
                    <TooltipRow>
                      rate <b>{perMTok(m.CostUSD, m.Tokens)}</b>
                    </TooltipRow>
                    <TooltipRow>
                      sessions <b>{fmtInt(m.Sessions)}</b>
                    </TooltipRow>
                  </>,
                )
              }
              onMouseLeave={hide}
            />
          );
        })}
      </ClipRect>
      {models.map((m) => {
        if (!labeled.has(m.Model)) return null;
        const cx = xScale(Math.max(m.Tokens, xLo));
        const cy = yScale(Math.max(m.CostUSD, yLo));
        const r = rScale(m.Sessions);
        const label = labelOf(m.Model);
        const placeRight = cx + r + 8 < W - 16;
        return (
          <text
            key={`${m.Model}-label`}
            x={placeRight ? cx + r + 6 : cx - r - 6}
            y={cy + 4}
            className="scatter-label"
            textAnchor={placeRight ? "start" : "end"}
          >
            {label}
          </text>
        );
      })}
    </ChartSvg>
  );
}

export function ModelCostInstrument({ trends }: { trends: Trends }) {
  const models = trends.ModelCost;
  if (models.length === 0) return null;
  const { colorOf, labelOf } = modelStyle(models);
  const costliest = models[0];
  let totalCost = 0;
  let totalTokens = 0;
  for (const m of models) {
    totalCost += m.CostUSD;
    totalTokens += m.Tokens;
  }
  const blended = perMTok(totalCost, totalTokens);

  return (
    <section
      className="instrument"
      id="modelcost"
      aria-labelledby="modelcost-h"
    >
      <div className="instrument-head">
        <h2 id="modelcost-h">Model cost</h2>
      </div>
      <div className="panel">
        <StatStrip>
          <Stat label="models in window" value={formatCount(models.length)} />
          <Stat label="blended rate" value={blended} />
          {costliest && (
            <Stat
              label="highest spend"
              value={
                <span title={costliest.Model}>{labelOf(costliest.Model)}</span>
              }
            />
          )}
        </StatStrip>
        <div className="overflow-x">
          <div style={{ minWidth: 480 }}>
            <ModelCostChart models={models} />
          </div>
        </div>
        <Legend
          items={models.map((m) => ({
            color: colorOf(m.Model),
            label: labelOf(m.Model),
            title: m.Model,
          }))}
        />
        <p className="chart-footnote">
          Spend versus billed tokens in this window. The dashed line is the
          fleet's blended rate; a model above it cost more per token than the
          window as a whole.
        </p>
      </div>
    </section>
  );
}
