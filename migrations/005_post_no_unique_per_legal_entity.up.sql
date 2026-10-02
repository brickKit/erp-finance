-- post_no 的唯一范围是"同一个法人"：每个法人各有一套账，last_post_seq 计数器本来就
-- 是 (period, legal_entity_id) 一行，post_no 的格式 P-<期间>-<序号> 不含法人。
-- 全局唯一会让第二个法人在任何期间的第一张凭证都撞上第一个法人的同号凭证。
DROP INDEX IF EXISTS finance_journal_entries_post_no_uniq;
CREATE UNIQUE INDEX IF NOT EXISTS finance_journal_entries_post_no_uniq
  ON finance_journal_entries (legal_entity_id, post_no) WHERE post_no != '';
