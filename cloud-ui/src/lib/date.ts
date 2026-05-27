// Format an ISO-8601 timestamp as a locale-aware date string for display.
// Falls back to the raw input when the value can't be parsed so we never
// render "Invalid Date" — easier to spot bad payloads from the server.
export function fmtDate(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}
