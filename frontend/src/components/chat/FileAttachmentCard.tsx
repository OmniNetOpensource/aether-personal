// 本文件展示聊天消息与输入框中的可下载文件卡片：标明文件名与体积，链接直达后端签名的私有 OSS 下载地址。

import { formatFileSize } from '../../lib/messageAttachments.js';
import type { MessageFile } from '../../lib/messageAttachments.js';

// FileAttachmentCard 渲染单个文件附件；紧凑模式用于输入框芯片，完整卡片用于消息气泡。
export function FileAttachmentCard({
  file,
  compact = false,
}: {
  file: MessageFile;
  compact?: boolean;
}) {
  const icon = (
    <svg className="file-card-icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8l-5-5Z"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinejoin="round"
      />
      <path d="M14 3v5h5" stroke="currentColor" strokeWidth="1.7" strokeLinejoin="round" />
    </svg>
  );

  if (compact) {
    return (
      <span className="composer-file" title={`${file.name} (${formatFileSize(file.size)})`}>
        {icon}
        <span className="composer-file-name">{file.name}</span>
        <span className="composer-file-size">{formatFileSize(file.size)}</span>
      </span>
    );
  }

  if (!file.url) {
    return (
      <div className="file-card is-unavailable" aria-label={`文件 ${file.name}`}>
        {icon}
        <div className="file-card-meta">
          <span className="file-card-name">{file.name}</span>
          <span className="file-card-size">{formatFileSize(file.size)} · 链接暂不可用</span>
        </div>
      </div>
    );
  }

  return (
    <a
      className="file-card"
      href={file.url}
      target="_blank"
      rel="noopener noreferrer"
      download={file.name}
      aria-label={`下载 ${file.name}（${formatFileSize(file.size)}）`}
    >
      {icon}
      <div className="file-card-meta">
        <span className="file-card-name">{file.name}</span>
        <span className="file-card-size">{formatFileSize(file.size)}</span>
      </div>
      <svg className="file-card-download" viewBox="0 0 24 24" fill="none" aria-hidden="true">
        <path
          d="M12 4v11m0 0-4-4m4 4 4-4M5 19h14"
          stroke="currentColor"
          strokeWidth="1.8"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </a>
  );
}
