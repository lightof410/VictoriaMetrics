import { ReactNode, useMemo } from "react";
import { TopQueryColumn } from "../TopQueryPanel/TopQueryPanel";
import { humanizeSeconds } from "../../../utils/time";
import { formatBytes } from "../../../utils/bytes";

type UseTopQueriesColumns = {
  maxLifetime: string;
};

const MAX_VISIBLE_ADDRESSES = 2;

const formatAddresses = (values?: string[], truncated?: boolean): ReactNode => {
  if (!values || values.length === 0) return "-";

  const visible = values.slice(0, MAX_VISIBLE_ADDRESSES);
  const rest = values.length - visible.length;
  const title = values.join("\n") + (truncated ? "\n..." : "");

  return (
    <span
      className="vm-top-queries-table__addresses"
      title={title}
    >
      {visible.join(", ")}
      {rest > 0 && <span className="vm-top-queries-table__addresses-more">{` +${rest}`}</span>}
    </span>
  );
};

export const useTopQueriesColumns = ({ maxLifetime }: UseTopQueriesColumns) => {
  return useMemo(() => {
    const queryCol: TopQueryColumn = {
      key: "query"
    };

    const timeRangeCol: TopQueryColumn = {
      key: "timeRange",
      sortBy: "timeRangeSeconds",
      title: "range",
      tooltip: "The time range between start and end of the query request. 'instant' means the query was executed at a single point in time without a time range"
    };

    const countCol: TopQueryColumn = {
      key: "count",
      tooltip: `The number of times the query was executed over the last ${maxLifetime}`,
    };

    const remoteAddrCol: TopQueryColumn = {
      key: "remoteAddrs",
      title: "ip",
      sortable: false,
      tooltip: `Remote addresses (without port) of the clients which executed the query over the last ${maxLifetime}. An address is the proxy or gateway address when the request comes through one, unless the X-Forwarded-For header is set`,
      format: (row) => formatAddresses(row.remoteAddrs, row.clientsTruncated)
    };

    const xForwardedForCol: TopQueryColumn = {
      key: "xForwardedFor",
      title: "x-forwarded-for",
      sortable: false,
      tooltip: `X-Forwarded-For headers sent with the requests over the last ${maxLifetime}. The value is provided by the client and may be spoofed`,
      format: (row) => formatAddresses(row.xForwardedFor, row.clientsTruncated)
    };

    const topBySumDuration: TopQueryColumn[] = [
      queryCol,
      {
        key: "sumDurationSeconds",
        title: "duration",
        tooltip: `Cumulative time spent executing the query across all its invocations over the last ${maxLifetime}`,
        format: (row) => humanizeSeconds(row.sumDurationSeconds)
      },
      timeRangeCol,
      countCol,
      remoteAddrCol,
      xForwardedForCol,
    ];

    const topByAvgDuration: TopQueryColumn[] = [
      queryCol,
      {
        key: "avgDurationSeconds",
        title: "duration",
        tooltip: `Average time spent executing the query over the last ${maxLifetime}`,
        format: (row) => humanizeSeconds(row.avgDurationSeconds)
      },
      timeRangeCol,
      countCol,
      remoteAddrCol,
      xForwardedForCol,
    ];

    const topByCount: TopQueryColumn[] = [
      queryCol,
      timeRangeCol,
      countCol,
      remoteAddrCol,
      xForwardedForCol,
    ];

    const topByAvgMemoryUsage: TopQueryColumn[] = [
      queryCol,
      {
        key: "avgMemoryBytes",
        title: "memory",
        tooltip: `Average memory used during query execution over the last ${maxLifetime}`,
        format: (row) => formatBytes(row.avgMemoryBytes)
      },
      timeRangeCol,
      countCol,
      remoteAddrCol,
      xForwardedForCol,
    ];

    return {
      topBySumDuration,
      topByAvgDuration,
      topByCount,
      topByAvgMemoryUsage,
    };
  }, [maxLifetime]);
};
