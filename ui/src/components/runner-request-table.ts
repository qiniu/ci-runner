export function stickyTableHeaderOffset({
  scrollportTop,
  tableTop,
  tableHeight,
  headerHeight,
}: {
  scrollportTop: number
  tableTop: number
  tableHeight: number
  headerHeight: number
}) {
  return Math.max(0, Math.min(scrollportTop - tableTop, tableHeight - headerHeight))
}
