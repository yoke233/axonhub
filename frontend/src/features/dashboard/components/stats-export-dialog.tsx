import { useMemo, useState } from 'react';
import { Download, FileDown, Search } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { Skeleton } from '@/components/ui/skeleton';
import { TimePeriodSelector } from '@/components/time-period-selector';
import { formatNumber } from '@/utils/format-number';
import { useGeneralSettings } from '../../system/data/system';
import {
  fetchDashboardExportRows,
  type DashboardExportDimension,
  type DashboardExportRow,
  type DashboardExportTable,
} from '../data/dashboard';

type ExportColumn = {
  key: keyof DashboardExportRow;
  label: string;
  align?: 'left' | 'right';
  format?: (value: DashboardExportRow[keyof DashboardExportRow]) => string;
};

function escapeCsvCell(value: string | number) {
  const text = String(value);
  if (!/[",\r\n]/.test(text)) return text;
  return `"${text.replace(/"/g, '""')}"`;
}

function downloadCsv(filename: string, rows: DashboardExportRow[], columns: ExportColumn[]) {
  const header = columns.map((column) => escapeCsvCell(column.label)).join(',');
  const body = rows
    .map((row) =>
      columns
        .map((column) => {
          const value = row[column.key] ?? '';
          return escapeCsvCell(column.format ? column.format(value) : value);
        })
        .join(',')
    )
    .join('\n');
  const blob = new Blob([`\uFEFF${header}\n${body}`], { type: 'text/csv;charset=utf-8;' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

export function StatsExportDialog() {
  const { t, i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const [table, setTable] = useState<DashboardExportTable>('summary');
  const [dimension, setDimension] = useState<DashboardExportDimension>('channel');
  const [timeWindow, setTimeWindow] = useState('day');
  const [rows, setRows] = useState<DashboardExportRow[]>([]);
  const [hasQueried, setHasQueried] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const { data: generalSettings } = useGeneralSettings();

  const currencyCode = generalSettings?.currencyCode || 'USD';
  const locale = i18n.language.startsWith('zh') ? 'zh-CN' : 'en-US';

  const formatCurrency = (val: number) =>
    t('currencies.format', {
      val,
      currency: currencyCode,
      locale,
      minimumFractionDigits: 6,
      maximumFractionDigits: 6,
    });

  const totals = useMemo(
    () =>
      rows.reduce(
        (total, row) => ({
          requests: total.requests + row.requests,
          cost: total.cost + row.cost,
          totalTokens: total.totalTokens + row.totalTokens,
        }),
        { requests: 0, cost: 0, totalTokens: 0 }
      ),
    [rows]
  );

  const dimensionLabel = t(`dashboard.export.dimension.${dimension}`);
  const isSuccessRateTable = table === 'channelSuccessRate';

  const columns = useMemo<ExportColumn[]>(() => {
    const formatStatNumber = (value: DashboardExportRow[keyof DashboardExportRow]) =>
      typeof value === 'number' ? formatNumber(value) : String(value ?? '');
    const formatPercent = (value: DashboardExportRow[keyof DashboardExportRow]) =>
      typeof value === 'number' ? `${value.toFixed(1)}%` : '';
    const formatMoney = (value: DashboardExportRow[keyof DashboardExportRow]) =>
      typeof value === 'number' ? formatCurrency(value) : '';
    const baseName: ExportColumn = { key: 'name', label: dimensionLabel };

    if (table === 'requestCost') {
      return [
        baseName,
        { key: 'requests', label: t('dashboard.stats.requests'), align: 'right', format: formatStatNumber },
        { key: 'cost', label: t('dashboard.stats.totalCost'), align: 'right', format: formatMoney },
      ];
    }

    if (table === 'tokens') {
      return [
        baseName,
        { key: 'inputTokens', label: t('dashboard.stats.inputTokens'), align: 'right', format: formatStatNumber },
        { key: 'outputTokens', label: t('dashboard.stats.outputTokens'), align: 'right', format: formatStatNumber },
        { key: 'cachedTokens', label: t('dashboard.stats.cachedTokens'), align: 'right', format: formatStatNumber },
        { key: 'reasoningTokens', label: t('dashboard.stats.reasoningTokens'), align: 'right', format: formatStatNumber },
        { key: 'totalTokens', label: t('dashboard.stats.totalTokens'), align: 'right', format: formatStatNumber },
      ];
    }

    if (table === 'channelSuccessRate') {
      return [
        { key: 'name', label: t('dashboard.export.dimension.channel') },
        { key: 'type', label: t('dashboard.export.columns.type') },
        { key: 'requests', label: t('dashboard.export.columns.totalRequests'), align: 'right', format: formatStatNumber },
        { key: 'successCount', label: t('dashboard.export.columns.successCount'), align: 'right', format: formatStatNumber },
        { key: 'failedCount', label: t('dashboard.export.columns.failedCount'), align: 'right', format: formatStatNumber },
        { key: 'successRate', label: t('dashboard.export.columns.successRate'), align: 'right', format: formatPercent },
        { key: 'inputTokens', label: t('dashboard.stats.inputTokens'), align: 'right', format: formatStatNumber },
        { key: 'outputTokens', label: t('dashboard.stats.outputTokens'), align: 'right', format: formatStatNumber },
        { key: 'totalTokens', label: t('dashboard.stats.totalTokens'), align: 'right', format: formatStatNumber },
      ];
    }

    return [
      baseName,
      { key: 'requests', label: t('dashboard.stats.requests'), align: 'right', format: formatStatNumber },
      { key: 'cost', label: t('dashboard.stats.totalCost'), align: 'right', format: formatMoney },
      { key: 'inputTokens', label: t('dashboard.stats.inputTokens'), align: 'right', format: formatStatNumber },
      { key: 'outputTokens', label: t('dashboard.stats.outputTokens'), align: 'right', format: formatStatNumber },
      { key: 'cachedTokens', label: t('dashboard.stats.cachedTokens'), align: 'right', format: formatStatNumber },
      { key: 'reasoningTokens', label: t('dashboard.stats.reasoningTokens'), align: 'right', format: formatStatNumber },
      { key: 'totalTokens', label: t('dashboard.stats.totalTokens'), align: 'right', format: formatStatNumber },
    ];
  }, [dimensionLabel, table, t, formatCurrency]);

  const handleQuery = async () => {
    setIsLoading(true);
    setError(null);
    setHasQueried(true);

    try {
      const data = await fetchDashboardExportRows(table, isSuccessRateTable ? 'channel' : dimension, timeWindow);
      setRows(data);
    } catch (err) {
      setRows([]);
      setError(err instanceof Error ? err.message : t('dashboard.export.queryFailed'));
    } finally {
      setIsLoading(false);
    }
  };

  const handleExport = () => {
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    downloadCsv(`axonhub-${table}-${isSuccessRateTable ? 'channel' : dimension}-${stamp}.csv`, rows, columns);
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant='outline' size='sm' className='gap-2'>
          <Download className='h-4 w-4' />
          {t('dashboard.export.trigger')}
        </Button>
      </DialogTrigger>
      <DialogContent className='max-h-[88vh] overflow-hidden sm:max-w-5xl'>
        <DialogHeader>
          <DialogTitle>{t('dashboard.export.title')}</DialogTitle>
          <DialogDescription>{t('dashboard.export.description')}</DialogDescription>
        </DialogHeader>

        <div className='grid gap-3 rounded-md border bg-muted/20 p-3 md:grid-cols-[190px_180px_1fr_auto_auto] md:items-end'>
          <div className='space-y-1.5'>
            <div className='text-xs font-medium text-muted-foreground'>{t('dashboard.export.tableLabel')}</div>
            <Select
              value={table}
              onValueChange={(value) => {
                const nextTable = value as DashboardExportTable;
                setTable(nextTable);
                setRows([]);
                setHasQueried(false);
                if (nextTable === 'channelSuccessRate') setDimension('channel');
              }}
            >
              <SelectTrigger className='w-full'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value='summary'>{t('dashboard.export.table.summary')}</SelectItem>
                <SelectItem value='requestCost'>{t('dashboard.export.table.requestCost')}</SelectItem>
                <SelectItem value='tokens'>{t('dashboard.export.table.tokens')}</SelectItem>
                <SelectItem value='channelSuccessRate'>{t('dashboard.export.table.channelSuccessRate')}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className='space-y-1.5'>
            <div className='text-xs font-medium text-muted-foreground'>{t('dashboard.export.dimensionLabel')}</div>
            <Select
              value={isSuccessRateTable ? 'channel' : dimension}
              onValueChange={(value) => {
                setDimension(value as DashboardExportDimension);
                setRows([]);
                setHasQueried(false);
              }}
              disabled={isSuccessRateTable}
            >
              <SelectTrigger className='w-full'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value='channel'>{t('dashboard.export.dimension.channel')}</SelectItem>
                <SelectItem value='model'>{t('dashboard.export.dimension.model')}</SelectItem>
                <SelectItem value='apiKey'>{t('dashboard.export.dimension.apiKey')}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className='space-y-1.5'>
            <div className='text-xs font-medium text-muted-foreground'>{t('dashboard.export.timeWindowLabel')}</div>
            <TimePeriodSelector value={timeWindow} onChange={setTimeWindow} allowCustom />
          </div>

          <Button type='button' variant='secondary' onClick={handleQuery} disabled={isLoading} className='gap-2'>
            <Search className='h-4 w-4' />
            {isLoading ? t('dashboard.export.querying') : t('dashboard.export.query')}
          </Button>

          <Button type='button' onClick={handleExport} disabled={rows.length === 0 || isLoading} className='gap-2'>
            <FileDown className='h-4 w-4' />
            {t('dashboard.export.exportCsv')}
          </Button>
        </div>

        <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-4'>
          <div className='rounded-md border p-3'>
            <div className='text-xs text-muted-foreground'>{t('dashboard.export.summary.rows')}</div>
            <div className='font-mono text-lg font-semibold'>{formatNumber(rows.length)}</div>
          </div>
          <div className='rounded-md border p-3'>
            <div className='text-xs text-muted-foreground'>{t('dashboard.stats.requests')}</div>
            <div className='font-mono text-lg font-semibold'>{formatNumber(totals.requests)}</div>
          </div>
          <div className='rounded-md border p-3'>
            <div className='text-xs text-muted-foreground'>{t('dashboard.stats.totalTokens')}</div>
            <div className='font-mono text-lg font-semibold'>{formatNumber(totals.totalTokens)}</div>
          </div>
          <div className='rounded-md border p-3'>
            <div className='text-xs text-muted-foreground'>{t('dashboard.stats.totalCost')}</div>
            <div className='font-mono text-lg font-semibold'>{formatCurrency(totals.cost)}</div>
          </div>
        </div>

        {error && <div className='rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive'>{error}</div>}

        <div className='min-h-[280px] overflow-auto rounded-md border'>
          <Table>
            <TableHeader className='sticky top-0 bg-background'>
              <TableRow>
                {columns.map((column) => (
                  <TableHead key={column.key} className={column.align === 'right' ? 'text-right' : undefined}>
                    {column.label}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading ? (
                Array.from({ length: 6 }).map((_, index) => (
                  <TableRow key={index}>
                    <TableCell colSpan={columns.length}>
                      <Skeleton className='h-6 w-full' />
                    </TableCell>
                  </TableRow>
                ))
              ) : rows.length > 0 ? (
                rows.map((row) => (
                  <TableRow key={`${row.id}-${row.name}`}>
                    {columns.map((column, index) => {
                      const value = row[column.key] ?? '';
                      return (
                        <TableCell
                          key={column.key}
                          className={`${index === 0 ? 'max-w-[220px] truncate font-medium' : 'font-mono'} ${column.align === 'right' ? 'text-right' : ''}`}
                        >
                          {column.format ? column.format(value) : value}
                        </TableCell>
                      );
                    })}
                  </TableRow>
                ))
              ) : (
                <TableRow>
                  <TableCell colSpan={columns.length} className='h-32 text-center text-sm text-muted-foreground'>
                    {hasQueried ? t('dashboard.export.empty') : t('dashboard.export.waiting')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      </DialogContent>
    </Dialog>
  );
}
