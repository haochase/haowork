# DEC-TIMEOUT：保留历史超时约束

虚构客户纸舟商店曾遇到等待超时但最终结果迟到的构造问题。需求根因位于 `../scenario.json` 的 requirement.root_cause。即使当前调用链没有引用，也不能据此删除超时对账约束。

product-owner 负责决定；TASK-CONTRACT、TASK-REVIEW、TASK-DOCS 共同落实。要删除约束，先提出有来源的需求变更，取得 Human 审批，并重新绑定上下文与证据。此处没有实际客户材料或生产事故记录。
