// Package http 是 erp-finance 的 REST 面（对外路径前缀 /erp/finance，与
// assembly.yaml 的 edge_routes 一致）。不暴露 CheckPeriodOpen 与
// BatchGetCreditExposure：前者是组件之间期间锁的咨询入口，后者是给别的组件的
// gRPC 客户端一次取多个客户用的，都不是人的操作。
package http

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/erp-finance/v2/backend/internal/repo"
	"github.com/brickKit/erp-finance/v2/backend/internal/service"
)

// RegisterRoutes 挂载业务路由，每条都带 assembly.yaml 里声明的权限键：
// erp.finance.view 管全部读，erp.finance.post 管手工过账与冲销，erp.finance.close
// 管关账 / 反关账 / 锁定期间，erp.finance.manage_access 管法人访问授权。
//
// legal_entity 维的数据范围（谁能看、能改哪个法人的账）不在 JWT 里：本组件自己查
// legal_entity_access 表（repo/access.go），读路径把授权列表下推进 SQL，写路径
// 额外核对请求里点名的 legal_entity_id 在不在授权范围内。
func RegisterRoutes(eng *gin.Engine, svc *service.Service) {
	g := eng.Group("/erp/finance")
	besdk.POST(g, "/periods/:period/close", "erp.finance.close", periodOpHandler(svc.ClosePeriod))
	besdk.POST(g, "/periods/:period/reopen", "erp.finance.close", periodOpHandler(svc.ReopenPeriod))
	besdk.POST(g, "/periods/:period/lock", "erp.finance.close", periodOpHandler(svc.LockPeriod))
	besdk.GET(g, "/credit-exposure/:customer_id", "erp.finance.view", getCreditExposureHandler(svc))
	besdk.GET(g, "/entries", "erp.finance.view", listEntriesHandler(svc))
	besdk.POST(g, "/entries", "erp.finance.post", postManualEntryHandler(svc))
	besdk.GET(g, "/entries/:id", "erp.finance.view", getEntryHandler(svc))
	besdk.POST(g, "/entries/:id/reverse", "erp.finance.post", reverseEntryHandler(svc))
	besdk.GET(g, "/ar-ledger", "erp.finance.view", listARLedgerHandler(svc))
	besdk.GET(g, "/ar-ledger/summary", "erp.finance.view", arLedgerSummaryHandler(svc))
	besdk.GET(g, "/legal-entity-access/:sub", "erp.finance.manage_access", listLegalEntityAccessHandler(svc))
	besdk.POST(g, "/legal-entity-access/:sub", "erp.finance.manage_access", grantLegalEntityAccessHandler(svc))
	besdk.DELETE(g, "/legal-entity-access/:sub/:legal_entity_id", "erp.finance.manage_access", revokeLegalEntityAccessHandler(svc))
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

// parseWindow 读列表的 created_after / created_before（RFC 3339）。不传就由
// repo 补默认的最近 90 天窗口；格式不对返回错误（handler 回 400），不悄悄忽略。
func parseWindow(c *gin.Context) (after, before time.Time, err error) {
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"created_after", &after}, {"created_before", &before}} {
		raw := c.Query(p.name)
		if raw == "" {
			continue
		}
		t, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			return after, before, fmt.Errorf("%s 不是 RFC 3339 时间：%q", p.name, raw)
		}
		*p.dst = t
	}
	return after, before, nil
}

func listEntriesHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		after, before, err := parseWindow(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		pageSize, _ := strconv.Atoi(c.Query("page_size"))
		out, err := svc.ListEntries(c.Request.Context(), repo.ListInput{
			Cursor: c.Query("cursor"), PageSize: pageSize,
			Period: c.Query("period"), StatusFilter: c.Query("status_filter"),
			SourceDocID: c.Query("source_doc_id"), SourceDocType: c.Query("source_doc_type"),
			CreatedAfter: after, CreatedBefore: before,
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
		after, before, err := parseWindow(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		pageSize, _ := strconv.Atoi(c.Query("page_size"))
		out, err := svc.ListARLedger(c.Request.Context(), repo.ListARLedgerInput{
			Cursor: c.Query("cursor"), PageSize: pageSize, CustomerID: c.Query("customer_id"),
			CreatedAfter: after, CreatedBefore: before,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		dtos := make([]gin.H, 0, len(out.Entries))
		for _, e := range out.Entries {
			dtos = append(dtos, gin.H{
				"id": e.ID, "customer_id": e.CustomerID, "customer_name": e.CustomerName, "entry_id": e.EntryID,
				"amount": e.Amount, "reconciled_amount": e.Reconciled, "outstanding": e.Outstanding,
				"due_date": e.DueDate, "created_at": e.CreatedAt.Format(rfc3339),
			})
		}
		c.JSON(http.StatusOK, gin.H{"entries": dtos, "next_cursor": out.NextCursor})
	}
}

func arLedgerSummaryHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		sum, err := svc.SummarizeARLedger(c.Request.Context(), c.Query("customer_id"))
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"as_of":            sum.AsOf,
			"total_receivable": sum.TotalReceivable,
			"total_reconciled": sum.TotalReconciled,
			"outstanding":      sum.Outstanding,
			"aging": gin.H{
				"d0_30": sum.Aging.D0To30, "d31_60": sum.Aging.D31To60,
				"d61_90": sum.Aging.D61To90, "d90_plus": sum.Aging.D90Plus,
			},
		})
	}
}

// ── legal_entity_access 管理（erp.finance.manage_access）──

func listLegalEntityAccessHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		ids, err := svc.ListLegalEntityAccess(c.Request.Context(), c.Param("sub"))
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, gin.H{"legal_entity_ids": ids})
	}
}

type grantLegalEntityAccessRequest struct {
	LegalEntityID string `json:"legal_entity_id" binding:"required"`
}

func grantLegalEntityAccessHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req grantLegalEntityAccessRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := svc.GrantLegalEntityAccess(c.Request.Context(), c.Param("sub"), req.LegalEntityID); err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.Status(http.StatusOK)
	}
}

func revokeLegalEntityAccessHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		err := svc.RevokeLegalEntityAccess(c.Request.Context(), c.Param("sub"), c.Param("legal_entity_id"))
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.Status(http.StatusOK)
	}
}
