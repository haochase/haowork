# 五端兼容合同

以 `../scenario.json` 的 platforms、tasks、checks 为准。各端都应检查 cancel、late-result、timeout-retry、duplicate-callback。超时仅终止本地等待，迟到结果仍需对账；取消不是退款；重复回调必须幂等。本文是预期，不是实际测试结果。

测试负责人应分别采集五端证据，并绑定需求、任务、Context、版本和实际文件摘要。静态样例校验不能代替设备、支付渠道或人工验收，当前均 NOT_RUN。
