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
	return s.repo.GetEntry(ctx, id)
}

func (s *Service) ListEntries(ctx context.Context, in repo.ListInput) (*repo.ListResult, error) {
	return s.repo.ListEntries(ctx, in)
}

func (s *Service) ListARLedger(ctx context.Context, in repo.ListARLedgerInput) (*repo.ListARLedgerResult, error) {
	return s.repo.ListARLedger(ctx, in)
}
