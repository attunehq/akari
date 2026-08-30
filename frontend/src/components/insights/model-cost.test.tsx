import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { ModelCost, Trends } from "../../types";
import { ModelCostInstrument } from "./model-cost";
import { TooltipHost } from "./tooltip";

function trends(models: ModelCost[]): Trends {
  return {
    Unit: "day",
    BucketStarts: ["2026-07-01T00:00:00Z"],
    Labels: ["Jul 1"],
    FleetMix: { Models: [], NewestModel: "", NewestFirst: -1 },
    ModelCost: models,
    Gallery: {
      Rows: [],
      Total: 0,
      MedianDurationS: 0,
      MedianCostUSD: 0,
      MedianCompletedCostUSD: 0,
      PriciestDurationS: 0,
      PriciestCostUSD: 0,
      LongestDurationS: 0,
      LongestCostUSD: 0,
    },
    Velocity: {
      ActiveHours: [0],
      WallHours: [0],
      ResponseP50: [0],
      ResponseP90: [0],
      ResponseP99: [0],
      MsgsPerMin: [0],
      ToolsPerMin: [0],
    },
    Tools: {
      Reliability: [],
      MixOrder: [],
      Mix: [{}],
      FailFleet: [0],
      FailWorst: [],
    },
    Churn: {
      ReEdits: [0],
      Files: [0],
      Tree: [],
      Clipped: 0,
      TotalReEdits: 0,
      TotalHotFiles: 0,
      Projects: 0,
    },
    Signals: {
      GradeShare: [{}],
      GPA: [0],
      ArchetypeShare: [{}],
      CompletedRate: [0],
      AbandonedRate: [0],
      OutcomeTotal: [0],
      CompletedCount: [0],
      AbandonedCount: [0],
      HygieneTerse: [0],
      HygieneRepeated: [0],
      HygieneNoCode: [0],
      HygieneUnstructured: [0],
      ContextResets: [0],
      ContextHistogram: [],
      ContextMarkers: [],
    },
    Economics: {
      CostCompleted: [0],
      CostAbandoned: [0],
      CostOther: [0],
      CacheSavings: [0],
      CacheHitRate: [0],
      CacheMeasured: [true],
      TotalSpend: 0,
      TotalAbandoned: 0,
      AbandonedSharePct: 0,
      TotalCacheSavings: 0,
      CacheHitRateLatest: 0,
    },
    Subagents: {
      DelegateShare: [0],
      CostShare: [0],
      FanoutOrder: [],
      FanoutRows: [{}],
      SessionsThatDelegatePct: 0,
      SubagentSessionsInWindow: 0,
      CostThroughSubagentsPct: 0,
      DeepestTree: 0,
    },
    Rhythm: { Cells: Array.from({ length: 7 }, () => Array(24).fill(0)) },
  };
}

const sample: ModelCost[] = [
  {
    Model: "claude-opus-4-8",
    CostUSD: 12,
    Tokens: 80_000,
    Sessions: 4,
  },
  {
    Model: "claude-sonnet-5",
    CostUSD: 3,
    Tokens: 200_000,
    Sessions: 8,
  },
];

describe("ModelCostInstrument", () => {
  it("renders the window's models as a cost × tokens scatter", () => {
    render(
      <TooltipHost>
        <ModelCostInstrument trends={trends(sample)} />
      </TooltipHost>,
    );

    expect(
      screen.getByRole("heading", { name: "Model cost" }),
    ).toBeInTheDocument();
    expect(screen.getByText("models in window")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    expect(screen.getByText("highest spend")).toBeInTheDocument();

    const legend = document.querySelector(".legend") as HTMLElement;
    expect(within(legend).getByText("opus-4-8")).toBeInTheDocument();
    expect(within(legend).getByText("sonnet-5")).toBeInTheDocument();
    expect(document.querySelectorAll(".scatter-dot")).toHaveLength(2);
  });

  it("titles the hover tooltip with the full model id and the window figures", () => {
    const { container } = render(
      <TooltipHost>
        <ModelCostInstrument trends={trends(sample)} />
      </TooltipHost>,
    );

    fireEvent.mouseMove(container.querySelector(".scatter-dot") as Element, {
      clientX: 10,
      clientY: 10,
    });

    expect(
      document.querySelector(".chart-tooltip .tt-title")?.textContent,
    ).toBe("claude-opus-4-8");
    const rows = [...document.querySelectorAll(".chart-tooltip .tt-row")].map(
      (row) => row.textContent,
    );
    expect(rows.some((row) => row?.includes("cost"))).toBe(true);
    expect(rows.some((row) => row?.includes("tokens"))).toBe(true);
    expect(rows.some((row) => row?.includes("sessions"))).toBe(true);
  });

  it("keeps the full id when stripping the vendor prefix would give two models one label", () => {
    render(
      <TooltipHost>
        <ModelCostInstrument
          trends={trends([
            {
              Model: "claude-sonnet-5",
              CostUSD: 5,
              Tokens: 10_000,
              Sessions: 1,
            },
            { Model: "sonnet-5", CostUSD: 2, Tokens: 8_000, Sessions: 1 },
          ])}
        />
      </TooltipHost>,
    );

    const legend = document.querySelector(".legend") as HTMLElement;
    expect(within(legend).getByText("claude-sonnet-5")).toBeInTheDocument();
    expect(within(legend).getByText("sonnet-5")).toBeInTheDocument();
  });

  it("renders nothing when the window has no model spend", () => {
    render(
      <TooltipHost>
        <ModelCostInstrument trends={trends([])} />
      </TooltipHost>,
    );
    expect(
      screen.queryByRole("heading", { name: "Model cost" }),
    ).not.toBeInTheDocument();
  });
});
