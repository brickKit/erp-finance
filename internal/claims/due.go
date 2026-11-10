package claims

import (
	"context"
	"encoding/base64"
	"strings"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/db"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
)

// DueItems answers a legal entity's payables and credit balances due on or before a date with an available balance,
// earliest due first, for erp/payment's proposals (read only); a page at most 200, 50 by default, the cursor the last
// (due date, ID) seen.
func DueItems(m *core.Base, ctx context.Context, in *financev3.DueItemsRequest) (*financev3.DueItemsResponse, error) {
	switch {
	case !core.UUIDForm.MatchString(in.GetLegalEntityId()):
		return nil, core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	case !core.DateForm.MatchString(in.GetDueOnOrBefore()):
		return nil, core.Invalid("due_on_or_before", "due_on_or_before is YYYY-MM-DD")
	case in.GetKind() != "" && in.GetKind() != "payable" && in.GetKind() != "credit_balance":
		return nil, core.Invalid("kind", "kind is payable or credit_balance")
	case in.GetPartnerId() != "" && !core.UUIDForm.MatchString(in.GetPartnerId()):
		return nil, core.Invalid("partner_id", "partner_id is a partner's ID")
	case in.GetLimit() < 0 || in.GetLimit() > 200:
		return nil, core.Invalid("limit", "limit is 1-200")
	}
	kinds := []string{"payable", "credit_balance"}
	if in.GetKind() != "" {
		kinds = []string{in.GetKind()}
	}
	limit := int(in.GetLimit())
	if limit == 0 {
		limit = 50
	}
	afterDate, afterID := "0001-01-01", "00000000-0000-0000-0000-000000000000"
	if c := in.GetCursor(); c != "" {
		b, err := base64.RawURLEncoding.DecodeString(c)
		parts := strings.SplitN(string(b), "|", 2)
		if err != nil || len(parts) != 2 || !core.DateForm.MatchString(parts[0]) || !core.UUIDForm.MatchString(parts[1]) {
			return nil, core.Invalid("cursor", "cursor is the next_cursor of an earlier page")
		}
		afterDate, afterID = parts[0], parts[1]
	}
	out := &financev3.DueItemsResponse{}
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, kind, legal_entity_id::text, partner_id::text, currency,
			trim_scale(amount)::text, trim_scale(settled)::text, trim_scale(pay_claimed)::text,
			trim_scale(amount - settled - pay_claimed)::text, due_date::text, source_kind, coalesce(source_id, ''),
			coalesce(source_number, '')
			FROM open_item WHERE legal_entity_id = $1 AND kind = ANY($2) AND state = 'open' AND due_date <= $3::date
			AND amount - settled - pay_claimed > 0 AND ($4 = '' OR partner_id = nullif($4, '')::uuid)
			AND (due_date, id) > ($5::date, $6::uuid)
			ORDER BY due_date, id LIMIT $7`, in.GetLegalEntityId(), kinds, in.GetDueOnOrBefore(), in.GetPartnerId(),
			afterDate, afterID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			it := &financev3.OpenItem{}
			if err := rows.Scan(&it.Id, &it.Kind, &it.LegalEntityId, &it.PartnerId, &it.Currency, &it.Amount, &it.Settled,
				&it.PayClaimed, &it.Available, &it.DueDate, &it.SourceKind, &it.SourceId, &it.SourceNumber); err != nil {
				return err
			}
			out.Items = append(out.Items, it)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(last.GetDueDate() + "|" + last.GetId()))
	}
	return out, nil
}
