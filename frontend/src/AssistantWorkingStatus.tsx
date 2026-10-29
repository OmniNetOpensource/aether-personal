// 本文件是助手处理中状态组件：在聊天底部显示思考动画，或有 recap 时显示行动摘要文字。

export function AssistantWorkingStatus({ recap }: { recap: string }) {
  if (recap) {
    return (
      <div className="recap" role="status" aria-label={recap}>
        <span className="recap-text">{recap}</span>
      </div>
    );
  }
  return (
    <div className="thinking" role="status" aria-label="阿芒正在处理">
      <div className="thinking-dots" aria-hidden="true">
        <span className="dot" />
        <span className="dot dot-two" />
        <span className="dot dot-three" />
      </div>
    </div>
  );
}
