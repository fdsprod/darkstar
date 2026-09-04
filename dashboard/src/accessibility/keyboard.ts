/** Returns the next tab index for the WAI-ARIA horizontal tab keyboard pattern. */
export function tabKeyTarget(current: number, key: string, count: number): number | undefined {
  if (!Number.isInteger(current) || !Number.isInteger(count) || current < 0 || current >= count || count < 1) return undefined;
  switch (key) {
    case "ArrowRight": return (current + 1) % count;
    case "ArrowLeft": return (current - 1 + count) % count;
    case "Home": return 0;
    case "End": return count - 1;
    default: return undefined;
  }
}
