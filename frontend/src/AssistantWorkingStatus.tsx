// 本文件是助手处理中状态组件：Main 正在处理时，在聊天底部显示思考动画。

export function AssistantWorkingStatus() {
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
