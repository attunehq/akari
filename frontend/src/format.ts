// Every Intl formatter below is built once at module scope rather than per call.
// Constructing one resolves the locale and compiles a pattern, which costs an order
// of magnitude more than the format() it wraps, and these run several times per feed
// row on lists that grow without bound as the reader pages.
const costFmt = new Intl.NumberFormat(undefined, {
  style: "currency",
  currency: "USD",
});
const countFmt = new Intl.NumberFormat(undefined, {
  notation: "standard",
  maximumFractionDigits: 1,
});
const countCompactFmt = new Intl.NumberFormat(undefined, {
  notation: "compact",
  maximumFractionDigits: 1,
});
const percentFmt = new Intl.NumberFormat(undefined, {
  style: "percent",
  maximumFractionDigits: 1,
});
const dateTimeFmt = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});
const relativeFmt = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

// Largest first: relativeTime picks the first unit the delta reaches.
const RELATIVE_UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ["year", 365 * 86_400_000],
  ["month", 30 * 86_400_000],
  ["day", 86_400_000],
  ["hour", 3_600_000],
  ["minute", 60_000],
];

export function formatCost(value: number): string {
  // Sub-cent costs keep four decimals so a cheap session reads as $0.0042
  // rather than rounding to a meaningless $0.00, mirroring the server's
  // FmtCost so every cost figure reads identically at any magnitude. Above a
  // cent, one fixed precision keeps a column of costs scannable: no magnitude
  // tier to make $39.0 look like a different kind of number than $9.96.
  if (value > 0 && value < 0.01) return `$${value.toFixed(4)}`;
  return costFmt.format(value);
}

// formatTokens mirrors the server's FmtTokensCompact (B/M/k suffixes, one decimal)
// so token figures read identically wherever they appear. The server's FmtTokens is
// a different formatter: thousands-separated, for places that show the full number.
export function formatTokens(value: number): string {
  if (value >= 1e9) return `${(value / 1e9).toFixed(1)}B`;
  if (value >= 1e6) return `${(value / 1e6).toFixed(1)}M`;
  if (value >= 1e3) return `${(value / 1e3).toFixed(1)}k`;
  return String(value);
}

export function formatCount(value: number): string {
  return (value >= 10_000 ? countCompactFmt : countFmt).format(value);
}

export function formatPercent(value: number): string {
  return percentFmt.format(value);
}

export function formatTime(value: string | null): string {
  if (!value) return "-";
  return dateTimeFmt.format(new Date(value));
}

export function relativeTime(value: string | null): string {
  if (!value) return "unknown";
  const delta = new Date(value).getTime() - Date.now();
  const abs = Math.abs(delta);
  const unit = RELATIVE_UNITS.find(([, size]) => abs >= size) ?? [
    "second",
    1_000,
  ];
  return relativeFmt.format(Math.round(delta / unit[1]), unit[0]);
}

export function sessionTokens(session: {
  TotalInput: number;
  TotalOutput: number;
  TotalCacheRead: number;
  TotalCacheWrite: number;
}): number {
  return (
    session.TotalInput +
    session.TotalOutput +
    session.TotalCacheRead +
    session.TotalCacheWrite
  );
}
