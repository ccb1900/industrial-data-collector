// Dynamic Metadata rendering: keys are never hard-coded, so new business
// fields (line/station/product/batch/shift/...) need no UI change.
export function metadataEntries(
  metadata: Record<string, string>
): Array<[string, string]> {
  return Object.keys(metadata)
    .sort()
    .map((k) => [k, metadata[k]]);
}
