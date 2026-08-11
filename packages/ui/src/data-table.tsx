import { ChevronLeft, ChevronRight, SlidersHorizontal } from "lucide-react";
import type { Key, ReactNode } from "react";
import { Button, Card, Select } from "./primitives";
import { cn } from "./utils";

export type DataTableColumn<T> = {
  key: string;
  header: ReactNode;
  render: (record: T) => ReactNode;
  className?: string;
  mobileLabel?: string;
};

export type DataTableProps<T> = {
  columns: DataTableColumn<T>[];
  data: T[];
  getRowKey: (record: T) => Key;
  empty: ReactNode;
  onRowClick?: (record: T) => void;
  page?: number;
  pages?: number;
  rowsPerPage?: number;
  rowsLabel: string;
  previousLabel: string;
  nextLabel: string;
  onPageChange?: (page: number) => void;
  className?: string;
};

export function DataTable<T>({
  columns,
  data,
  getRowKey,
  empty,
  onRowClick,
  page = 1,
  pages = 1,
  rowsPerPage = 25,
  rowsLabel,
  previousLabel,
  nextLabel,
  onPageChange,
  className
}: DataTableProps<T>) {
  return (
    <Card className={cn("mp-table-card", className)}>
      <div className="mp-table-scroll">
        <table className="mp-table">
          <thead>
            <tr>{columns.map((column) => <th className={column.className} key={column.key} scope="col">{column.header}</th>)}</tr>
          </thead>
          <tbody>
            {data.length === 0 && <tr><td className="mp-table__empty" colSpan={columns.length}>{empty}</td></tr>}
            {data.map((record) => (
              <tr
                className={cn(onRowClick && "is-clickable")}
                key={getRowKey(record)}
                onClick={() => onRowClick?.(record)}
                onKeyDown={(event) => {
                  if (onRowClick && (event.key === "Enter" || event.key === " ")) {
                    event.preventDefault();
                    onRowClick(record);
                  }
                }}
                tabIndex={onRowClick ? 0 : undefined}
              >
                {columns.map((column) => (
                  <td className={column.className} data-label={column.mobileLabel ?? column.header} key={column.key}>
                    {column.render(record)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="mp-table-pagination">
        <div className="mp-table-pagination__size">
          <SlidersHorizontal aria-hidden="true" size={14} />
          <Select aria-label={`${rowsPerPage} ${rowsLabel}`} defaultValue={rowsPerPage}>
            <option value={10}>10</option><option value={25}>25</option><option value={50}>50</option>
          </Select>
          <span>{rowsLabel}</span>
        </div>
        <span className="mp-table-pagination__count">{page} / {pages}</span>
        <div className="mp-table-pagination__buttons">
          <Button aria-label={previousLabel} disabled={page <= 1} onClick={() => onPageChange?.(page - 1)} size="icon" variant="quiet"><ChevronLeft size={17} /></Button>
          <Button aria-label={nextLabel} disabled={page >= pages} onClick={() => onPageChange?.(page + 1)} size="icon" variant="quiet"><ChevronRight size={17} /></Button>
        </div>
      </div>
    </Card>
  );
}
