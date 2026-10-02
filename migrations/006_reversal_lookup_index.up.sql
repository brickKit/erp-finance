-- 冲销前要查"这张凭证是否已经被冲销过"（一张凭证只能冲销一次）。凭证头永不归档、
-- 只会越来越大，这个查询不能扫全表。
CREATE INDEX IF NOT EXISTS finance_journal_entries_reversal_of
  ON finance_journal_entries (source_doc_id) WHERE source_doc_type = 'reversal';
