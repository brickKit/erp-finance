// Package grpc 实现 erp.finance.v1.FinanceService——内部 gRPC 面（§2.1）。
// HTTP 与 gRPC 共用同一个 service.Service，业务逻辑只写一遍。
package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	financev1 "github.com/brickKit/erp-finance/gen/erp/finance/v1"

	"github.com/brickKit/erp-finance/backend/internal/repo"
	"github.com/brickKit/erp-finance/backend/internal/service"
)

type server struct {
	financev1.UnimplementedFinanceServiceServer
	svc *service.Service
}

func New(svc *service.Service) financev1.FinanceServiceServer {
	return &server{svc: svc}
}

func toProtoPeriodStatus(s string) financev1.PeriodStatus {
	switch s {
	case repo.PeriodOpen:
		return financev1.PeriodStatus_PERIOD_STATUS_OPEN
	case repo.PeriodClosed:
		return financev1.PeriodStatus_PERIOD_STATUS_CLOSED
	case repo.PeriodLocked:
		return financev1.PeriodStatus_PERIOD_STATUS_LOCKED
	default:
		return financev1.PeriodStatus_PERIOD_STATUS_UNSPECIFIED
	}
}

func (s *server) CheckPeriodOpen(ctx context.Context, req *financev1.CheckPeriodOpenRequest) (*financev1.CheckPeriodOpenResponse, error) {
	status, err := s.svc.CheckPeriodOpen(ctx, req.Period, req.LegalEntityId)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &financev1.CheckPeriodOpenResponse{Status: toProtoPeriodStatus(status)}, nil
}

func (s *server) ClosePeriod(ctx context.Context, req *financev1.ClosePeriodRequest) (*financev1.ClosePeriodResponse, error) {
	status, err := s.svc.ClosePeriod(ctx, repo.PeriodOpInput{
		IdempotencyKey: req.IdempotencyKey, Period: req.Period, LegalEntityID: req.LegalEntityId,
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &financev1.ClosePeriodResponse{Status: toProtoPeriodStatus(status)}, nil
}

func (s *server) ReopenPeriod(ctx context.Context, req *financev1.ReopenPeriodRequest) (*financev1.ReopenPeriodResponse, error) {
	status, err := s.svc.ReopenPeriod(ctx, repo.PeriodOpInput{
		IdempotencyKey: req.IdempotencyKey, Period: req.Period, LegalEntityID: req.LegalEntityId,
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &financev1.ReopenPeriodResponse{Status: toProtoPeriodStatus(status)}, nil
}

func (s *server) LockPeriod(ctx context.Context, req *financev1.LockPeriodRequest) (*financev1.LockPeriodResponse, error) {
	status, err := s.svc.LockPeriod(ctx, repo.PeriodOpInput{
		IdempotencyKey: req.IdempotencyKey, Period: req.Period, LegalEntityID: req.LegalEntityId,
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &financev1.LockPeriodResponse{Status: toProtoPeriodStatus(status)}, nil
}

func toProtoCreditExposure(ce *repo.CreditExposure) *financev1.CreditExposure {
	return &financev1.CreditExposure{CustomerId: ce.CustomerID, Exposure: ce.Exposure, Version: ce.Version}
}

func (s *server) GetCreditExposure(ctx context.Context, req *financev1.GetCreditExposureRequest) (*financev1.CreditExposure, error) {
	ce, err := s.svc.GetCreditExposure(ctx, req.CustomerId)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return toProtoCreditExposure(ce), nil
}

// BatchGetCreditExposure 是防 N+1 的唯一合法调用方式（§3.8）。
func (s *server) BatchGetCreditExposure(ctx context.Context, req *financev1.BatchGetCreditExposureRequest) (*financev1.BatchGetCreditExposureResponse, error) {
	exposures, err := s.svc.BatchGetCreditExposure(ctx, req.CustomerIds)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	out := make([]*financev1.CreditExposure, 0, len(exposures))
	for _, ce := range exposures {
		out = append(out, toProtoCreditExposure(ce))
	}
	return &financev1.BatchGetCreditExposureResponse{Exposures: out}, nil
}

func toProtoEntryStatus(s string) financev1.EntryStatus {
	switch s {
	case repo.EntryDraft:
		return financev1.EntryStatus_ENTRY_STATUS_DRAFT
	case repo.EntryPosted:
		return financev1.EntryStatus_ENTRY_STATUS_POSTED
	default:
		return financev1.EntryStatus_ENTRY_STATUS_UNSPECIFIED
	}
}

func fromProtoLines(lines []*financev1.JournalEntryLine) []repo.Line {
	out := make([]repo.Line, 0, len(lines))
	for _, l := range lines {
		out = append(out, repo.Line{AccountCode: l.AccountId, Debit: l.Debit, Credit: l.Credit, Memo: l.Memo})
	}
	return out
}

func toProtoEntry(e *repo.Entry) *financev1.JournalEntry {
	lines := make([]*financev1.JournalEntryLine, 0, len(e.Lines))
	for _, l := range e.Lines {
		lines = append(lines, &financev1.JournalEntryLine{
			AccountId: l.AccountCode, Debit: l.Debit, Credit: l.Credit, Memo: l.Memo,
		})
	}
	out := &financev1.JournalEntry{
		Id: e.ID, EntryNo: e.EntryNo, PostNo: e.PostNo, Period: e.Period, LegalEntityId: e.LegalEntityID,
		Status: toProtoEntryStatus(e.Status), SourceComponent: e.SourceComponent, SourceDocType: e.SourceDocType,
		SourceDocId: e.SourceDocID, SourceRevision: e.SourceRevision, Lines: lines, Memo: e.Memo,
		Version: e.Version, CreatedAt: timestamppb.New(e.CreatedAt),
	}
	if e.PostedAt != nil {
		out.PostedAt = timestamppb.New(*e.PostedAt)
	}
	return out
}

func (s *server) PostManualEntry(ctx context.Context, req *financev1.PostManualEntryRequest) (*financev1.PostManualEntryResponse, error) {
	entry, err := s.svc.PostManualEntry(ctx, repo.PostManualEntryInput{
		IdempotencyKey: req.IdempotencyKey, LegalEntityID: req.LegalEntityId,
		Lines: fromProtoLines(req.Lines), Memo: req.Memo,
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &financev1.PostManualEntryResponse{Entry: toProtoEntry(entry)}, nil
}

func (s *server) ReverseEntry(ctx context.Context, req *financev1.ReverseEntryRequest) (*financev1.ReverseEntryResponse, error) {
	entry, err := s.svc.ReverseEntry(ctx, repo.ReverseEntryInput{
		IdempotencyKey: req.IdempotencyKey, EntryID: req.EntryId, Reason: req.Reason,
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &financev1.ReverseEntryResponse{Entry: toProtoEntry(entry)}, nil
}

func (s *server) GetEntry(ctx context.Context, req *financev1.GetEntryRequest) (*financev1.JournalEntry, error) {
	entry, err := s.svc.GetEntry(ctx, req.Id)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return toProtoEntry(entry), nil
}

func (s *server) ListEntries(ctx context.Context, req *financev1.ListEntriesRequest) (*financev1.ListEntriesResponse, error) {
	in := repo.ListInput{Cursor: req.Cursor, PageSize: int(req.PageSize), Period: req.Period}
	if req.StatusFilter != financev1.EntryStatus_ENTRY_STATUS_UNSPECIFIED {
		switch req.StatusFilter {
		case financev1.EntryStatus_ENTRY_STATUS_DRAFT:
			in.StatusFilter = repo.EntryDraft
		case financev1.EntryStatus_ENTRY_STATUS_POSTED:
			in.StatusFilter = repo.EntryPosted
		}
	}
	if req.CreatedAfter != nil {
		in.CreatedAfter = req.CreatedAfter.AsTime()
	}
	if req.CreatedBefore != nil {
		in.CreatedBefore = req.CreatedBefore.AsTime()
	}
	out, err := s.svc.ListEntries(ctx, in)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	entries := make([]*financev1.JournalEntry, 0, len(out.Entries))
	for _, e := range out.Entries {
		entries = append(entries, toProtoEntry(e))
	}
	return &financev1.ListEntriesResponse{Entries: entries, NextCursor: out.NextCursor}, nil
}

func (s *server) ListARLedger(ctx context.Context, req *financev1.ListARLedgerRequest) (*financev1.ListARLedgerResponse, error) {
	in := repo.ListARLedgerInput{Cursor: req.Cursor, PageSize: int(req.PageSize), CustomerID: req.CustomerId}
	if req.CreatedAfter != nil {
		in.CreatedAfter = req.CreatedAfter.AsTime()
	}
	if req.CreatedBefore != nil {
		in.CreatedBefore = req.CreatedBefore.AsTime()
	}
	out, err := s.svc.ListARLedger(ctx, in)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	entries := make([]*financev1.ARLedgerEntry, 0, len(out.Entries))
	for _, e := range out.Entries {
		entries = append(entries, &financev1.ARLedgerEntry{
			Id: e.ID, CustomerId: e.CustomerID, EntryId: e.EntryID, Amount: e.Amount,
			ReconciledAmount: e.Reconciled, CreatedAt: timestamppb.New(e.CreatedAt),
		})
	}
	return &financev1.ListARLedgerResponse{Entries: entries, NextCursor: out.NextCursor}, nil
}
