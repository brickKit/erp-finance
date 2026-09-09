// Package service 是 erp-finance 的业务规则层：入参校验 + 给 http/grpc
// 一个不依赖 repo 内部细节的稳定入口。真正的过账逻辑、期间权威判定、
// 幂等约束都在 repo 层随 SQL 一起做，这一层依然薄（同 erp-inventory
// 的判据）。
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/erp-finance/backend/internal/repo"
)

var ErrInvalidArgument = errors.New("参数不合法")

type Service struct {
	repo   *repo.Repo
	logger *slog.Logger
}

func New(r *repo.Repo, logger *slog.Logger) *Service {
	return &Service{repo: r, logger: logger}
}

// allowedLegalEntityIDs 是 ClosePeriod/ReopenPeriod/LockPeriod/
// PostManualEntry/ReverseEntry/GetEntry/ListEntries/ListARLedger 八个
// REST 端点共用的一步——同 erp-inventory 的 allowedWarehouseIDs，只用于
// REST 端点：CheckPeriodOpen/GetCreditExposure/BatchGetCreditExposure
// 是组件间 gRPC 协议，ctx 里没有经过 besdk.RequirePermission 验签的
// Claims，调 ScopeOf 会 panic（阶段三 Task 6 讨论定案：gRPC 侧数据权限
// 透传是更大的独立工作，不在本任务范围）。
func (s *Service) allowedLegalEntityIDs(ctx context.Context) ([]string, error) {
	sub := besdk.ScopeOf(ctx).Owner
	return s.repo.LegalEntityIDsFor(ctx, sub)
}

// ── 会计期间 ──

func (s *Service) CheckPeriodOpen(ctx context.Context, period, legalEntityID string) (string, error) {
	if period == "" || legalEntityID == "" {
		return "", fmt.Errorf("%w: period/legal_entity_id 不能为空", ErrInvalidArgument)
	}
	return s.repo.CheckPeriodOpen(ctx, period, legalEntityID)
}

func validatePeriodOp(in repo.PeriodOpInput) error {
	if in.IdempotencyKey == "" {
		return fmt.Errorf("%w: idempotency_key 不能为空", ErrInvalidArgument)
	}
	if in.Period == "" || in.LegalEntityID == "" {
		return fmt.Errorf("%w: period/legal_entity_id 不能为空", ErrInvalidArgument)
	}
	return nil
}

func (s *Service) ClosePeriod(ctx context.Context, in repo.PeriodOpInput) (string, error) {
	if err := validatePeriodOp(in); err != nil {
		return "", err
	}
	allowed, err := s.allowedLegalEntityIDs(ctx)
	if err != nil {
		return "", err
	}
	in.AllowedLegalEntityIDs = allowed
	status, err := s.repo.ClosePeriod(ctx, in)
	if err != nil {
		s.logger.Error("关账失败", "period", in.Period, "legal_entity_id", in.LegalEntityID, "error", err)
		return "", err
	}
	return status, nil
}

func (s *Service) ReopenPeriod(ctx context.Context, in repo.PeriodOpInput) (string, error) {
	if err := validatePeriodOp(in); err != nil {
		return "", err
	}
	allowed, err := s.allowedLegalEntityIDs(ctx)
	if err != nil {
		return "", err
	}
	in.AllowedLegalEntityIDs = allowed
	status, err := s.repo.ReopenPeriod(ctx, in)
	if err != nil {
		s.logger.Error("反关账失败", "period", in.Period, "legal_entity_id", in.LegalEntityID, "error", err)
		return "", err
	}
	return status, nil
}

func (s *Service) LockPeriod(ctx context.Context, in repo.PeriodOpInput) (string, error) {
	if err := validatePeriodOp(in); err != nil {
		return "", err
	}
	allowed, err := s.allowedLegalEntityIDs(ctx)
	if err != nil {
		return "", err
	}
	in.AllowedLegalEntityIDs = allowed
	status, err := s.repo.LockPeriod(ctx, in)
	if err != nil {
		s.logger.Error("锁定期间失败", "period", in.Period, "legal_entity_id", in.LegalEntityID, "error", err)
		return "", err
	}
	return status, nil
}

// ── 信用额度 ──

func (s *Service) GetCreditExposure(ctx context.Context, customerID string) (*repo.CreditExposure, error) {
	return s.repo.GetCreditExposure(ctx, customerID)
}

func (s *Service) BatchGetCreditExposure(ctx context.Context, customerIDs []string) ([]*repo.CreditExposure, error) {
	return s.repo.BatchGetCreditExposure(ctx, customerIDs)
}

// ── 凭证 ──

func validateLines(lines []repo.Line) error {
	if len(lines) == 0 {
		return fmt.Errorf("%w: lines 不能为空", ErrInvalidArgument)
	}
	for _, l := range lines {
		if l.AccountCode == "" {
			return fmt.Errorf("%w: account_id 不能为空", ErrInvalidArgument)
		}
	}
	return nil
}

func (s *Service) PostManualEntry(ctx context.Context, in repo.PostManualEntryInput) (*repo.Entry, error) {
	if in.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key 不能为空", ErrInvalidArgument)
	}
	if in.LegalEntityID == "" {
		return nil, fmt.Errorf("%w: legal_entity_id 不能为空", ErrInvalidArgument)
	}
	if err := validateLines(in.Lines); err != nil {
		return nil, err
	}
	allowed, err := s.allowedLegalEntityIDs(ctx)
	if err != nil {
		return nil, err
	}
	in.AllowedLegalEntityIDs = allowed
	entry, err := s.repo.PostManualEntry(ctx, in)
	if err != nil {
		s.logger.Error("手工过账失败", "legal_entity_id", in.LegalEntityID, "error", err)
		return nil, err
	}
	return entry, nil
}

func (s *Service) ReverseEntry(ctx context.Context, in repo.ReverseEntryInput) (*repo.Entry, error) {
	if in.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key 不能为空", ErrInvalidArgument)
	}
	if in.EntryID == "" {
		return nil, fmt.Errorf("%w: entry_id 不能为空", ErrInvalidArgument)
	}
	allowed, err := s.allowedLegalEntityIDs(ctx)
	if err != nil {
		return nil, err
	}
	in.AllowedLegalEntityIDs = allowed
	entry, err := s.repo.ReverseEntry(ctx, in)
	if err != nil {
		s.logger.Error("红字冲销失败", "entry_id", in.EntryID, "error", err)
		return nil, err
	}
	return entry, nil
}

func (s *Service) GetEntry(ctx context.Context, id string) (*repo.Entry, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: id 不能为空", ErrInvalidArgument)
	}
	allowed, err := s.allowedLegalEntityIDs(ctx)
	if err != nil {
		return nil, err
	}
	return s.repo.GetEntry(ctx, id, allowed)
}

func (s *Service) ListEntries(ctx context.Context, in repo.ListInput) (*repo.ListResult, error) {
	allowed, err := s.allowedLegalEntityIDs(ctx)
	if err != nil {
		return nil, err
	}
	in.AllowedLegalEntityIDs = allowed
	return s.repo.ListEntries(ctx, in)
}

func (s *Service) ListARLedger(ctx context.Context, in repo.ListARLedgerInput) (*repo.ListARLedgerResult, error) {
	allowed, err := s.allowedLegalEntityIDs(ctx)
	if err != nil {
		return nil, err
	}
	in.AllowedLegalEntityIDs = allowed
	return s.repo.ListARLedger(ctx, in)
}

// ── legal_entity_access 管理（阶段三 Task 6）──

func (s *Service) ListLegalEntityAccess(ctx context.Context, sub string) ([]string, error) {
	if sub == "" {
		return nil, fmt.Errorf("%w: sub 不能为空", ErrInvalidArgument)
	}
	return s.repo.LegalEntityIDsFor(ctx, sub)
}

func (s *Service) GrantLegalEntityAccess(ctx context.Context, sub, legalEntityID string) error {
	if sub == "" || legalEntityID == "" {
		return fmt.Errorf("%w: sub/legal_entity_id 不能为空", ErrInvalidArgument)
	}
	return s.repo.GrantLegalEntityAccess(ctx, sub, legalEntityID)
}

func (s *Service) RevokeLegalEntityAccess(ctx context.Context, sub, legalEntityID string) error {
	if sub == "" || legalEntityID == "" {
		return fmt.Errorf("%w: sub/legal_entity_id 不能为空", ErrInvalidArgument)
	}
	return s.repo.RevokeLegalEntityAccess(ctx, sub, legalEntityID)
}
