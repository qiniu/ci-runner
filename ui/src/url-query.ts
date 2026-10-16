// '/' is data inside a query value, not a path separator. Restore only that
// escape after encoding; literal '%2F', '&', '#', '+' and '=' remain distinct.
export function readableQuery(query: URLSearchParams): string {
  return query.toString().replace(/%2F/gi, "/")
}
