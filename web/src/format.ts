const DATE_TIME = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});

const RELATIVE_TIME = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes)) return "—";
  if (Math.abs(bytes) < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = -1;
  do {
    value /= 1024;
    unit += 1;
  } while (Math.abs(value) >= 1024 && unit < units.length - 1);
  return `${value.toFixed(value >= 10 ? 1 : 2)} ${units[unit]}`;
}

export interface FormattedTime {
  relative: string;
  absolute: string;
}

export function formatTime(value: string | null | undefined): FormattedTime | null {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return { relative: value, absolute: value };
  const deltaSeconds = (date.getTime() - Date.now()) / 1000;
  const spans: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ["year", 31_536_000],
    ["month", 2_592_000],
    ["week", 604_800],
    ["day", 86_400],
    ["hour", 3_600],
    ["minute", 60],
    ["second", 1],
  ];
  const [unit, seconds] = spans.find(([, span]) => Math.abs(deltaSeconds) >= span) ?? spans[spans.length - 1];
  return {
    relative: RELATIVE_TIME.format(Math.round(deltaSeconds / seconds), unit),
    absolute: DATE_TIME.format(date),
  };
}

export function formatDuration(milliseconds: number): string {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return "—";
  const totalSeconds = Math.floor(milliseconds / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes === 0) return `${seconds}s`;
  return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
