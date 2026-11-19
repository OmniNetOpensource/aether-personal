// 本文件维护 Virtuoso 列表的 firstItemIndex，在顶部加载更早历史、裁剪头部旧消息或重置列表时保持已有消息索引不变。

import { useLayoutEffect, useRef } from 'react';
import type { ChatMessage } from '../../lib/ChatController.js';

// 服务端按游标分页，无法预知全部历史条数；从足够大的起点出发，向上翻页递减、裁剪头部递增。
const initialFirstItemIndex = 1_000_000_000;

interface CommittedIndexAnchor {
  firstItemIndex: number;
  ids: string[];
}

// useVirtuosoFirstItemIndex 通过首条重合消息反推新的 firstItemIndex，避免在渲染期间修改 ref，兼容 Strict Mode。
export function useVirtuosoFirstItemIndex(messages: readonly ChatMessage[]): number {
  const committed = useRef<CommittedIndexAnchor>({
    firstItemIndex: initialFirstItemIndex,
    ids: [],
  });
  let firstItemIndex = initialFirstItemIndex;
  const previous = committed.current;
  if (messages.length > 0 && previous.ids.length > 0) {
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
  useLayoutEffect(() => {
    committed.current = {
      firstItemIndex,
      ids: messages.map((message) => message.id),
    };
  }, [firstItemIndex, messages]);
  return firstItemIndex;
}
