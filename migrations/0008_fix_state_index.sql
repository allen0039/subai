-- P2-02: 将 failed_after_dispatch 从 active-state 索引中排除
-- 审查发现：第4份迁移新增了 failed_after_dispatch 终态，但 idx_requests_state 的
-- partial predicate 没有将其列为终态。失败记录会持续占据为活跃状态优化的索引。

DROP INDEX IF EXISTS idx_requests_state;

CREATE INDEX idx_requests_state ON requests(state)
WHERE state NOT IN (
    'completed',
    'rejected',
    'audit_failed',
    'cancelled_before_dispatch',
    'failed_before_dispatch',
    'failed_after_dispatch'
);
