/*
 * Truncated machine identifiers for pickers and tables.
 *
 * A UUID in an option label is noise; the first 8 characters are enough to tell two
 * rows apart while the full value stays available in the `title` attribute (see
 * `CopyableHash`). Keep the separator configurable so a caller that shows both ends
 * (hash-like values) can reuse the same helper.
 */
export function shortId(value: string, head = 8, tail = 0): string {
  const trimmed = value.trim();
  if (trimmed.length === 0) return '';
  if (trimmed.length <= head + tail + 1) return trimmed;
  const tailPart = tail > 0 ? '…' + trimmed.slice(-tail) : '';
  return trimmed.slice(0, head) + tailPart;
}
