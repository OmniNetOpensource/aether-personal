// 本文件是消息时间分隔组件：渲染日期与时间分隔标签，并提供同一天判断供消息列表分组。

export function MessageTimeSeparator({ createdAt }: { createdAt: Date }) {
  return (
    <p className="date">
      <time dateTime={createdAt.toISOString()}>
        {dateLabel(createdAt)} {timeLabel(createdAt)}
      </time>
    </p>
  );
}

export function sameDay(a: Date, b: Date) {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

function timeLabel(date: Date) {
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
}

function dateLabel(date: Date) {
  const today = new Date(),
    yesterday = new Date();
  yesterday.setDate(today.getDate() - 1);
  return sameDay(date, today)
    ? '今天'
    : sameDay(date, yesterday)
      ? '昨天'
      : `${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`;
}
