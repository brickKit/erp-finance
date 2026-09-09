// legal_entity_access——阶段三 Task 6 的 legal_entity 维数据权限分配表
// （见 004_create_legal_entity_access.up.sql 顶部注释）。这三个函数是
// 本组件唯一读写这张表的入口，被读接口（过滤 List/Get）与写接口的
// "点名的法人是否在授权范围内"校验、以及管理接口（grant/revoke）共用。
package repo

import (
	"context"
	"database/sql"
	"fmt"

	besdk "github.com/brickKit/be-sdk-go"
)

// LegalEntityIDsFor 查 sub 当前能访问的全部法人 id——没有任何分配时
// 返回空切片（同 erp-inventory 的 WarehouseIDsFor：空列表=谁都看不见，
// 纯 SQL `= ANY('{}')` 天然实现，不需要特判）。
func (r *Repo) LegalEntityIDsFor(ctx context.Context, sub string) ([]string, error) {
	var ids []string
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT legal_entity_id FROM legal_entity_access WHERE sub = $1 ORDER BY legal_entity_id`, sub)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("查 legal_entity_access: %w", err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

// GrantLegalEntityAccess 幂等授予——重复授予不报错。legalEntityID 不做
// 存在性校验：本组件不持有法人主数据（阶段二只有一个自由文本
// 'default'，见 004 迁移顶部注释），这里只管"谁能看这个字符串过滤出来
// 的数据"，不管这个字符串本身是不是"合法的法人"。
func (r *Repo) GrantLegalEntityAccess(ctx context.Context, sub, legalEntityID string) error {
	if legalEntityID == "" {
		return fmt.Errorf("%w: legal_entity_id 不能为空", ErrInvalidArgument)
	}
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO legal_entity_access (sub, legal_entity_id) VALUES ($1, $2)
			ON CONFLICT (sub, legal_entity_id) DO NOTHING`, sub, legalEntityID)
		return err
	})
	if err != nil {
		return fmt.Errorf("授予法人访问权限: %w", err)
	}
	return nil
}

// RevokeLegalEntityAccess 幂等撤销——撤销一条本来就不存在的分配不报错。
func (r *Repo) RevokeLegalEntityAccess(ctx context.Context, sub, legalEntityID string) error {
	err := besdk.WithTx(ctx, r.db, r.role, r.schema, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM legal_entity_access WHERE sub = $1 AND legal_entity_id = $2`, sub, legalEntityID)
		return err
	})
	if err != nil {
		return fmt.Errorf("撤销法人访问权限: %w", err)
	}
	return nil
}
