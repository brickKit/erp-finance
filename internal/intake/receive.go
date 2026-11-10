package intake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	masterdata "github.com/brickKit/erp-finance/v3/internal/masterdata"
	settlement "github.com/brickKit/erp-finance/v3/internal/settlement"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/work"
	"github.com/jackc/pgx/v5"
)

// JobPostFact posts one received fact (AR03).
const JobPostFact = "post-fact"

type postArgs struct {
	FactID string `json:"fact_id"`
}

// OnFact receives an incremental fact (protocol 05, common rules; AR02, AR03): in the consumer's transaction, the
// dedup record by fact ID and content digest and the post-fact work item; nothing is posted here (posting takes
// evidence from master data, which a transaction never calls). The same fact again is acknowledged; the same ID with
// another content is acknowledged and logged (first batch, docs/design.md AR02).
func OnFact(m *core.Base, ctx context.Context, tx db.Tx, e events.Received) error {
	var head struct {
		FactID string `json:"fact_id"`
	}
	if err := json.Unmarshal(e.Data, &head); err != nil || !core.UUIDForm.MatchString(head.FactID) {
		m.RT.Logger.Error("a fact without a fact_id is dropped", "type", e.Type, "event", e.ID)
		return nil
	}
	digest, err := digestOf(e.Data)
	if err != nil {
		return err
	}
	source := strings.TrimPrefix(e.Source, "urn:be:")
	var inserted bool
	err = tx.QueryRow(ctx, `INSERT INTO fact_received (fact_id, type, source, digest, data) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (fact_id) DO NOTHING RETURNING true`, head.FactID, e.Type, source, digest, e.Data).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		var known string
		if err := tx.QueryRow(ctx, `SELECT digest FROM fact_received WHERE fact_id = $1`, head.FactID).Scan(&known); err != nil {
			return err
		}
		if known != digest {
			m.RT.Logger.Error("a fact came again with another content; the first is kept", "event", "fact_digest_mismatch",
				"fact_id", head.FactID, "type", e.Type, "source", source)
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = m.RT.Work.Enqueue(ctx, tx, JobPostFact, postArgs{FactID: head.FactID})
	return err
}

// digestOf is the SHA-256 of the payload's canonical JSON (keys sorted, numbers as written): the same fact
// redelivered has the same digest however its producer spaced it.
func digestOf(data []byte) (string, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// received is a fact_received row.
type received struct {
	FactID string
	Type   string
	Source string
	Data   []byte
	State  string
}

// poster posts one kind of fact in the posting transaction and answers its voucher.
type poster func(m *core.Base, ctx context.Context, tx db.Tx, f core.Received) (string, error)

func posterOf(typ string) poster {
	switch {
	case typ == core.InvoiceIssued:
		return postInvoice
	case strings.HasSuffix(typ, core.FundFactSuffix):
		return settlement.PostFund
	case strings.HasSuffix(typ, core.AccountingFactSuffix):
		return postAccounting
	}
	return nil
}

// PostFact posts a received fact (AR03): the evidence of its legal entity outside any transaction, then in one
// transaction the fact's posting by its type and the fact marked posted; one already posted is done. A legal entity
// that has not mapped the rule's roles, or a correction before the fact it corrects, waits: the item retries without
// spending its attempts.
func PostFact(m *core.Base, ctx context.Context, r *work.Run) error {
	var a postArgs
	if err := json.Unmarshal(r.Args, &a); err != nil {
		return work.Permanent(err)
	}
	var f received
	err := m.RT.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT fact_id::text, type, source, data::text, state FROM fact_received WHERE fact_id = $1`,
			a.FactID).Scan(&f.FactID, &f.Type, &f.Source, &f.Data, &f.State)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return work.Permanent(errors.New("intake: no received fact " + a.FactID))
	}
	if err != nil || f.State == "posted" {
		return err
	}
	post := posterOf(f.Type)
	if post == nil {
		return work.Permanent(errors.New("intake: no posting for " + f.Type))
	}
	in, err := evidence(m, ctx, f)
	if err != nil {
		return err
	}
	err = r.Complete(ctx, func(ctx context.Context, tx db.Tx) error {
		if pending, err := claim(ctx, tx, f.FactID); err != nil || !pending {
			return err
		}
		voucher, err := post(m, ctx, tx, in)
		if err != nil {
			return err
		}
		return posted(ctx, tx, f.FactID, voucher)
	})
	if errs.Is(err, core.ID, "MAPPING_MISSING") || errors.Is(err, core.ErrWaitForOriginal) {
		return work.Wait(err)
	}
	return err
}

// evidence reads the fact's legal entity and its base currency's places from master data.
func evidence(m *core.Base, ctx context.Context, f received) (core.Received, error) {
	in := core.Received{FactID: f.FactID, Type: f.Type, Source: f.Source, Data: f.Data}
	var head struct {
		LegalEntityID string `json:"legal_entity_id"`
	}
	if err := json.Unmarshal(f.Data, &head); err != nil || !core.UUIDForm.MatchString(head.LegalEntityID) {
		return in, work.Permanent(errors.New("intake: fact " + f.FactID + " names no legal entity"))
	}
	le, err := masterdata.LegalEntityOf(m, ctx, head.LegalEntityID)
	if err != nil {
		return in, err
	}
	in.LegalEntityID, in.LegalEntityCode, in.BaseCurrency = le.ID, le.Code, le.BaseCurrency
	in.BaseScale, err = masterdata.CurrencyScale(m, ctx, le.BaseCurrency)
	return in, err
}

// claim locks the fact in the posting transaction: false when another run posted it meanwhile.
func claim(ctx context.Context, tx db.Tx, factID string) (bool, error) {
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM fact_received WHERE fact_id = $1 FOR UPDATE`, factID).Scan(&state); err != nil {
		return false, err
	}
	return state == "pending", nil
}

// posted marks the fact posted with its voucher, in the transaction that posted it.
func posted(ctx context.Context, tx db.Tx, factID, voucherID string) error {
	_, err := tx.Exec(ctx, `UPDATE fact_received SET state = 'posted', voucher_id = $2, posted_at = now() WHERE fact_id = $1`,
		factID, voucherID)
	return err
}
