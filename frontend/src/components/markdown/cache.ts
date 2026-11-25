// 本文件提供轻量有界 LRU 缓存，供 Markdown 语法树与代码高亮结果在虚拟滚动重挂载时复用。

// createLruCache 创建按最近访问顺序淘汰最旧条目的内存缓存。
export function createLruCache<V>(maxSize: number) {
  const entries = new Map<string, V>();
  return {
    get(key: string): V | undefined {
      if (!entries.has(key)) return undefined;
      const cached = entries.get(key)!;
      entries.delete(key);
      entries.set(key, cached);
      return cached;
    },
    set(key: string, value: V) {
      if (entries.has(key)) {
        entries.delete(key);
      } else if (entries.size >= maxSize) {
        const oldestKey = entries.keys().next().value;
        if (oldestKey !== undefined) entries.delete(oldestKey);
      }
      entries.set(key, value);
    },
  };
}
