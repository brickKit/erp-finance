-- 回到全局唯一。已经有两个法人同号的凭证时这条会失败——那正是 up 修掉的情形，
-- 回滚前要先人工处理那些凭证。
DROP INDEX IF EXISTS finance_journal_entries_post_no_uniq;
CREATE UNIQUE INDEX finance_journal_entries_post_no_uniq
  ON finance_journal_entries (post_no) WHERE post_no != '';
