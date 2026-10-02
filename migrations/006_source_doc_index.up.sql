-- 按源单找凭证：冲销前查"这张凭证是否已经被冲销过"（一张凭证只能冲销一次），
-- 以及 GET /entries?source_doc_id=&source_doc_type=（订单详情查看对应凭证）。
-- 凭证头永不归档、只会越来越大，这两个查询不能扫全表。
CREATE INDEX IF NOT EXISTS finance_journal_entries_source_doc
  ON finance_journal_entries (source_doc_id, source_doc_type);
