-- 阶段三 Task 6：legal_entity 维数据权限"谁能访问哪个法人"的分配表——
-- 同 erp-inventory 的 warehouse_access（阶段三 Task 6 讨论定案：分配
-- 数据归数据的宿主组件自己维护，不归 infra-authz、不进 JWT）。
--
-- ⚠️ legal_entity_id 是自由文本（阶段二只有一个默认法人 'default'，
-- 没有独立的法人主数据表——本组件不持有法人主数据，"法人"是别的地方
-- 的概念，本组件只借用这个字符串做过滤维度），所以这张表不建外键，
-- 与 warehouse_access 引用 warehouses.id 的情形不同。
CREATE TABLE legal_entity_access (
    sub             TEXT        NOT NULL,
    legal_entity_id TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (sub, legal_entity_id)
);

CREATE INDEX legal_entity_access_sub ON legal_entity_access (sub);
