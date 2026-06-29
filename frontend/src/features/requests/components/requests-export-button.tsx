import { useMemo, useState } from 'react';
import { format } from 'date-fns';
import { Download, Loader2 } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { extractNumberID } from '@/lib/utils';
import { useSelectedProjectId } from '@/stores/projectStore';
import { Button } from '@/components/ui/button';
import { useRequestPermissions } from '../../../hooks/useRequestPermissions';
import { fetchAllRequestsForExport, type ExportRequestNode } from '../data/requests';

interface ExportColumn {
  label: string;
  value: (row: ExportRequestNode) => string | number;
}

function escapeCsvCell(value: string | number) {
  const text = String(value ?? '');
  if (!/[",\r\n]/.test(text)) return text;
  return `"${text.replace(/"/g, '""')}"`;
}

function buildCsv(rows: ExportRequestNode[], columns: ExportColumn[]) {
  const header = columns.map((column) => escapeCsvCell(column.label)).join(',');
  const body = rows
    .map((row) => columns.map((column) => escapeCsvCell(column.value(row))).join(','))
    .join('\n');
  // Prefix BOM (U+FEFF) so Excel detects UTF-8 correctly.
  const bom = String.fromCharCode(0xfeff);
  return `${bom}${header}\n${body}`;
}

function usageOf(row: ExportRequestNode) {
  return row.usageLogs?.edges?.[0]?.node;
}

function downloadCsv(filename: string, content: string) {
  const blob = new Blob([content], { type: 'text/csv;charset=utf-8;' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

interface RequestsExportButtonProps {
  where?: Record<string, any>;
  headerWhere?: Record<string, any>;
}

export function RequestsExportButton({ where, headerWhere }: RequestsExportButtonProps) {
  const { t } = useTranslation();
  const permissions = useRequestPermissions();
  const projectId = useSelectedProjectId();
  const [isExporting, setIsExporting] = useState(false);

  const columns = useMemo<ExportColumn[]>(
    () => [
      { label: t('requests.export.columns.id'), value: (row) => `#${extractNumberID(row.id)}` },
      { label: t('requests.export.columns.createdAt'), value: (row) => format(new Date(row.createdAt), 'yyyy-MM-dd HH:mm:ss') },
      { label: t('requests.export.columns.modelId'), value: (row) => row.modelID ?? '' },
      { label: t('requests.export.columns.format'), value: (row) => row.format ?? '' },
      { label: t('requests.export.columns.source'), value: (row) => t(`requests.source.${row.source}`, row.source) },
      { label: t('requests.export.columns.status'), value: (row) => t(`requests.status.${row.status}`, row.status) },
      {
        label: t('requests.export.columns.stream'),
        value: (row) => (row.stream ? t('requests.stream.streaming') : t('requests.stream.nonStreaming')),
      },
      { label: t('requests.export.columns.reasoningEffort'), value: (row) => row.reasoningEffort ?? '' },
      ...(permissions.canViewChannels
        ? [{ label: t('requests.export.columns.channel'), value: (row: ExportRequestNode) => row.channel?.name ?? '' }]
        : []),
      ...(permissions.canViewApiKeys
        ? [{ label: t('requests.export.columns.apiKey'), value: (row: ExportRequestNode) => row.apiKey?.name ?? '' }]
        : []),
      { label: t('requests.export.columns.clientIP'), value: (row) => row.clientIP ?? '' },
      { label: t('requests.export.columns.promptTokens'), value: (row) => usageOf(row)?.promptTokens ?? '' },
      { label: t('requests.export.columns.completionTokens'), value: (row) => usageOf(row)?.completionTokens ?? '' },
      {
        label: t('requests.export.columns.totalTokens'),
        value: (row) => {
          const usage = usageOf(row);
          if (!usage) return '';
          return usage.totalTokens ?? (usage.promptTokens ?? 0) + (usage.completionTokens ?? 0);
        },
      },
      { label: t('requests.export.columns.cachedTokens'), value: (row) => usageOf(row)?.promptCachedTokens ?? '' },
      { label: t('requests.export.columns.writeCacheTokens'), value: (row) => usageOf(row)?.promptWriteCachedTokens ?? '' },
      { label: t('requests.export.columns.cost'), value: (row) => usageOf(row)?.totalCost ?? '' },
      { label: t('requests.export.columns.latencyMs'), value: (row) => row.metricsLatencyMs ?? '' },
      { label: t('requests.export.columns.firstTokenLatencyMs'), value: (row) => row.metricsFirstTokenLatencyMs ?? '' },
    ],
    [t, permissions]
  );

  const handleExport = async () => {
    setIsExporting(true);
    try {
      const { rows, truncated } = await fetchAllRequestsForExport({ where, headerWhere, permissions, projectId });

      if (rows.length === 0) {
        toast.info(t('requests.export.empty'));
        return;
      }

      const stamp = format(new Date(), 'yyyyMMdd-HHmmss');
      downloadCsv(`axonhub-requests-${stamp}.csv`, buildCsv(rows, columns));

      if (truncated) {
        toast.warning(t('requests.export.truncated', { count: rows.length }));
      } else {
        toast.success(t('requests.export.success', { count: rows.length }));
      }
    } catch {
      toast.error(t('requests.export.failed'));
    } finally {
      setIsExporting(false);
    }
  };

  return (
    <Button variant='outline' size='sm' onClick={handleExport} disabled={isExporting}>
      {isExporting ? <Loader2 className='mr-2 h-4 w-4 animate-spin' /> : <Download className='mr-2 h-4 w-4' />}
      {isExporting ? t('requests.export.exporting') : t('requests.export.trigger')}
    </Button>
  );
}
