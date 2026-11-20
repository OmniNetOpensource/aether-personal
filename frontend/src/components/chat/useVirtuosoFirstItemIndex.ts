// 本文件维护 Virtuoso 列表的 firstItemIndex，在顶部加载更早历史、裁剪头部旧消息或重置列表时保持已有消息索引不变。

import { useLayoutEffect, useRef } from 'react';
import type { ChatMessage } from '../../lib/ChatController.js';

// 服务端按游标分页，无法预知全部历史条数；从足够大的起点出发，向上翻页递减、裁剪头部递增。
const initialFirstItemIndex = 1_000_000_000;

interface CommittedIndexAnchor {
  firstItemIndex: number;
  messages: readonly ChatMessage[];
  ids: string[];
}

// useVirtuosoFirstItemIndex 通过首条重合消息反推新的 firstItemIndex，数组引用未变时直接复用上次结果。
export function useVirtuosoFirstItemIndex(messages: readonly ChatMessage[]): number {
  const committed = useRef<CommittedIndexAnchor>({
    firstItemIndex: initialFirstItemIndex,
    messages: [],
    ids: [],
  });
  const previous = committed.current;
  const sameMessages = messages === previous.messages;
  let firstItemIndex = sameMessages ? previous.firstItemIndex : initialFirstItemIndex;

  // 数组变更时才查找首条重合消息，保留它原来的虚拟列表索引。
  if (!sameMessages && messages.length > 0 && previous.ids.length > 0) {
    const previousIndexByID = new Map<string, number>();
    for (let i = 0; i < previous.ids.length; i += 1) {
      previousIndexByID.set(previous.ids[i], i);
    }
    for (let i = 0; i < messages.length; i += 1) {
      const previousIndex = previousIndexByID.get(messages[i].id);
      if (previousIndex !== undefined) {
        firstItemIndex = previous.firstItemIndex + previousIndex - i;
        break;
      }
    }
  }

  // 每次渲染都调用 Hook，提交新列表后再更新缓存。
  useLayoutEffect(() => {
    if (
      committed.current.messages === messages &&
      committed.current.firstItemIndex === firstItemIndex
    ) {
      return;
    }
    committed.current = {
      firstItemIndex,
      messages,
      ids: messages.map((message) => message.id),
    };
  }, [firstItemIndex, messages]);
  return firstItemIndex;
}
