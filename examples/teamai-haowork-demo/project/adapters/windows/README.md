# windows 适配审查

适配点：窗口关闭与协议回调。完整定义见 ../../scenario.json 的 platforms；关联 TASK-CONTRACT 与 DEC-TIMEOUT。

预期检查：cancel、late-result、timeout-retry、duplicate-callback。超时和取消不能直接转换为交易失败或退款；迟到结果需对账，重复回调需幂等。此处没有可收款实现，真实平台运行 NOT_RUN。

构造事件序列（来源：scenario.json 的 constructed_sequence）：t0 创建虚构订单并等待；t1 取消界面但保存交易标识；t2 本地等待超时，状态仍待核对；t3 恢复后收到迟到成功和重复成功回调。

本端恢复动作：关闭窗口后由协议回调唤起，恢复原交易标识并去重。使用原标识查询并幂等合并，不创建第二笔扣款或自动退款。

预期验收结果：只有一条最终结果；保留取消和等待超时的独立事实；迟到成功关联原订单；恢复前不宣称最终失败；重复回调不重复通知或扣款。这是待实施/审查的构造合同，原生设备测试 NOT_RUN。
