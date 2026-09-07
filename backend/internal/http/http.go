// Package http 是 erp-finance 的 REST 面（对外路径前缀 /erp/finance，
// 与 assembly.yaml 的 edge_routes 一致）。⚠️ 不暴露 CheckPeriodOpen/
// BatchGetCreditExposure——前者是组件间期间锁的咨询性入口，后者是给
// 其他组件 gRPC 客户端防 N+1 用的批量读优化（contracts/finance.openapi.yaml）。
package http

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/erp-finance/backend/internal/repo"
	"github.com/brickKit/erp-finance/backend/internal/service"
)

// RegisterRoutes 挂载业务路由。⚠️ 全部标 besdk.Public 是阶段二的刻意
// 状态（同 erp-inventory）：阶段三 infra-authz 上线后要把这几条改成
// assembly.yaml 里对应的真实权限键（erp.finance.view/post/close）。
func RegisterRoutes(eng *gin.Engine, svc *service.Service) {
	g := eng.Group("/erp/finance")
	besdk.POST(g, "/periods/:period/close", besdk.Public, periodOpHandler(svc.ClosePeriod))
	besdk.POST(g, "/periods/:period/reopen", besdk.Public, periodOpHandler(svc.ReopenPeriod))
	besdk.POST(g, "/periods/:period/lock", besdk.Public, periodOpHandler(svc.LockPeriod))
	besdk.GET(g, "/credit-exposure/:customer_id", besdk.Public, getCreditExposureHandler(svc))
	besdk.GET(g, "/entries", besdk.Public, listEntriesHandler(svc))
	besdk.POST(g, "/entries", besdk.Public, postManualEntryHandler(svc))
	besdk.GET(g, "/entries/:id", besdk.Public, getEntryHandler(svc))
	besdk.POST(g, "/entries/:id/reverse", besdk.Public, reverseEntryHandler(svc))
	besdk.GET(g, "/ar-ledger", besdk.Public, listARLedgerHandler(svc))
}

type periodOpRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	LegalEntityID  string `json:"legal_entity_id" binding:"required"`
}

// periodOpHandler 是 ClosePeriod/ReopenPeriod/LockPeriod 三个 REST
// handler 共用的骨架——三者的请求/响应形状完全一样，只是调的 service
// 方法不同。
func periodOpHandler(op func(ctx context.Context, in repo.PeriodOpInput) (string, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req periodOpRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		status, err := op(c.Request.Context(), repo.PeriodOpInput{
			IdempotencyKey: req.IdempotencyKey, Period: c.Param("period"), LegalEntityID: req.LegalEntityID,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": status})
	}
}

func getCreditExposureHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		ce, err := svc.GetCreditExposure(c.Request.Context(), c.Param("customer_id"))
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, gin.H{"customer_id": ce.CustomerID, "exposure": ce.Exposure, "version": ce.Version})
	}
}

type lineDTO struct {
	AccountID string `json:"account_id" binding:"required"`
	Debit     string `json:"debit"`
	Credit    string `json:"credit"`
	Memo      string `json:"memo"`
}

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

func toEntryDTO(e *repo.Entry) gin.H {
	lines := make([]gin.H, 0, len(e.Lines))
	for _, l := range e.Lines {
		lines = append(lines, gin.H{"account_id": l.AccountCode, "debit": l.Debit, "credit": l.Credit, "memo": l.Memo})
	}
	dto := gin.H{
		"id": e.ID, "entry_no": e.EntryNo, "post_no": e.PostNo, "period": e.Period,
		"legal_entity_id": e.LegalEntityID, "status": e.Status,
		"source_component": e.SourceComponent, "source_doc_type": e.SourceDocType,
		"source_doc_id": e.SourceDocID, "source_revision": e.SourceRevision,
		"lines": lines, "memo": e.Memo, "version": e.Version,
		"created_at": e.CreatedAt.Format(rfc3339),
	}
	if e.PostedAt != nil {
		dto["posted_at"] = e.PostedAt.Format(rfc3339)
	}
	return dto
}

type postManualEntryRequest struct {
	IdempotencyKey string    `json:"idempotency_key" binding:"required"`
	LegalEntityID  string    `json:"legal_entity_id" binding:"required"`
	Lines          []lineDTO `json:"lines" binding:"required"`
	Memo           string    `json:"memo"`
}

func postManualEntryHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req postManualEntryRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		lines := make([]repo.Line, 0, len(req.Lines))
		for _, l := range req.Lines {
			lines = append(lines, repo.Line{AccountCode: l.AccountID, Debit: l.Debit, Credit: l.Credit, Memo: l.Memo})
		}
		entry, err := svc.PostManualEntry(c.Request.Context(), repo.PostManualEntryInput{
			IdempotencyKey: req.IdempotencyKey, LegalEntityID: req.LegalEntityID, Lines: lines, Memo: req.Memo,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toEntryDTO(entry))
	}
}

func getEntryHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		entry, err := svc.GetEntry(c.Request.Context(), c.Param("id"))
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toEntryDTO(entry))
	}
}

type reverseEntryRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	Reason         string `json:"reason"`
}

func reverseEntryHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req reverseEntryRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		entry, err := svc.ReverseEntry(c.Request.Context(), repo.ReverseEntryInput{
			IdempotencyKey: req.IdempotencyKey, EntryID: c.Param("id"), Reason: req.Reason,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toEntryDTO(entry))
	}
}

func listEntriesHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		pageSize, _ := strconv.Atoi(c.Query("page_size"))
		out, err := svc.ListEntries(c.Request.Context(), repo.ListInput{
			Cursor: c.Query("cursor"), PageSize: pageSize,
			Period: c.Query("period"), StatusFilter: c.Query("status_filter"),
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		dtos := make([]gin.H, 0, len(out.Entries))
		for _, e := range out.Entries {
			dtos = append(dtos, toEntryDTO(e))
		}
		c.JSON(http.StatusOK, gin.H{"entries": dtos, "next_cursor": out.NextCursor})
	}
}

func listARLedgerHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		pageSize, _ := strconv.Atoi(c.Query("page_size"))
		out, err := svc.ListARLedger(c.Request.Context(), repo.ListARLedgerInput{
			Cursor: c.Query("cursor"), PageSize: pageSize, CustomerID: c.Query("customer_id"),
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		dtos := make([]gin.H, 0, len(out.Entries))
		for _, e := range out.Entries {
			dtos = append(dtos, gin.H{
				"id": e.ID, "customer_id": e.CustomerID, "entry_id": e.EntryID,
				"amount": e.Amount, "reconciled_amount": e.Reconciled,
				"created_at": e.CreatedAt.Format(rfc3339),
			})
		}
		c.JSON(http.StatusOK, gin.H{"entries": dtos, "next_cursor": out.NextCursor})
	}
}
