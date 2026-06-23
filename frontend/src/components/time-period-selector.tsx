import { useTranslation } from 'react-i18next';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { DateRangePicker, type DateTimeRangeValue } from '@/components/date-range-picker';
import { buildDateRangeWhereClause, DEFAULT_END_TIME, DEFAULT_START_TIME, normalizeDateTimeRangeValue, type TimeValue } from '@/utils/date-range';

export type TimePeriod = 'allTime' | 'month' | 'week' | 'day';
export type FastestTimeWindow = 'month' | 'week' | 'day';

const DEFAULT_PERIODS: readonly TimePeriod[] = ['allTime', 'month', 'week', 'day'];

interface TimePeriodSelectorProps<T extends string = TimePeriod> {
  value: T;
  onChange: (value: T) => void;
  periods?: readonly T[];
  allowCustom?: boolean;
}

function timeFromDate(date: Date): TimeValue {
  const pad = (value: number) => value.toString().padStart(2, '0');
  return {
    hh: pad(date.getHours()),
    mm: pad(date.getMinutes()),
    ss: pad(date.getSeconds()),
  };
}

function rangeToTimeWindow(range: DateTimeRangeValue) {
  const where = buildDateRangeWhereClause(range);
  return `custom|${where.createdAtGTE ?? ''}|${where.createdAtLTE ?? ''}`;
}

function timeWindowToRange(value: string): DateTimeRangeValue | undefined {
  if (!value.startsWith('custom|')) return undefined;

  const [, start, end] = value.split('|');
  const from = start ? new Date(start) : undefined;
  const to = end ? new Date(end) : undefined;

  return normalizeDateTimeRangeValue({
    from: from && !Number.isNaN(from.getTime()) ? from : undefined,
    to: to && !Number.isNaN(to.getTime()) ? to : undefined,
    startTime: from && !Number.isNaN(from.getTime()) ? timeFromDate(from) : DEFAULT_START_TIME,
    endTime: to && !Number.isNaN(to.getTime()) ? timeFromDate(to) : DEFAULT_END_TIME,
  });
}

export function TimePeriodSelector<T extends string>({ value, onChange, periods, allowCustom = false }: TimePeriodSelectorProps<T>) {
  const effectivePeriods = periods ?? DEFAULT_PERIODS as readonly T[];
  const { t } = useTranslation();
  const customRange = timeWindowToRange(value);

  return (
    <div className='flex flex-wrap items-center gap-1'>
      <Tabs value={customRange ? '' : value} onValueChange={(v) => onChange(v as T)}>
        <TabsList className='h-7 p-0.5'>
          {effectivePeriods.map((period) => (
            <TabsTrigger key={period} value={period} className='h-6 px-2 text-[10px]'>
              {t(`dashboard.stats.${period === 'allTime' ? 'all' : period}`)}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      {allowCustom && (
        <DateRangePicker
          value={customRange}
          onChange={(range) => {
            if (!range) onChange('allTime' as T);
          }}
          onConfirm={(range) => onChange(rangeToTimeWindow(range) as T)}
          className='min-w-0'
        />
      )}
    </div>
  );
}
