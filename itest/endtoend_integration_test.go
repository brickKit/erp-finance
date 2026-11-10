//go:build integration

package itest

// The finance component against PostgreSQL and the bus over its real surfaces (L2). The master data components are
// stand-ins that speak their published contracts over mTLS (standins_integration_test.go). Needs BE_SDK_TEST_PG_ADMIN
// and BE_SDK_TEST_NATS_URL, as be-sdk-go's integration tests. The member listens on 28087 (user) and 9097 (system); the
// stand-ins on 19120-19123.
// Run: go test -tags integration ./itest/ (make test-integration); the index of the steps is itest/AGENTS.md.
//
// Each step is in the file of its business flow; TestFinanceEndToEnd runs them in order.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	finance "github.com/brickKit/erp-finance/v3/finance"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/brickKit/erp-finance/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/events/bus"
	"github.com/brickKit/be-sdk-go/v3/events/jetstream"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	purchasev3 "github.com/brickKit/erp-purchase/contract/v3/gen/go/be/erp/purchase/v3"
	currencyv3 "github.com/brickKit/mdm-currency/contract/v3/gen/go/be/mdm/currency/v3"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	itPort     = "28087"
	itBase     = "http://localhost:" + itPort
	itSystem   = "localhost:9097"
	itOrg      = "localhost:19120"
	itPartner  = "localhost:19121"
	itCurrency = "localhost:19122"
	itPurchase = "localhost:19123"
	itSchema   = "erp_finance_it"
)

// fixture is the running member, its stand-ins and the world's IDs, shared by the steps.
type fixture struct {
	t     *testing.T
	ctx   context.Context
	w     *sectest.World
	admin *pgx.Conn
	alice client
	bob   client // a second accountant: approves what Alice makes
	org   *orgStandIn
	tr    events.Transport
	rt    *runtime.Runtime // the member's runtime

	le, le2, d1, d2, customer, supplier, blocked string
	// Shanghai's accounts by code (the chart step)
	accounts map[string]map[string]any
	// erp/purchase's match basis
	purchase *purchaseStandIn
	// the payable of the first supplier invoice (the supplierInvoices step), 1585.2 CNY
	payable string
}

func TestFinanceEndToEnd(t *testing.T) {
	fx := start(t)
	fx.notInThisVersion()
	fx.chart()
	fx.vouchers()
	fx.periods()
	fx.receivables()
	fx.supplierInvoices()
	fx.claims()
	fx.settlement()
	fx.accountingFacts()
	fx.manualItems()
}

// start makes the world, the stand-ins and mdm/org's stream, then migrates and runs the member.
func start(t *testing.T) *fixture {
	adminURL, natsURL := os.Getenv("BE_SDK_TEST_PG_ADMIN"), os.Getenv("BE_SDK_TEST_NATS_URL")
	if adminURL == "" || natsURL == "" {
		t.Fatal("BE_SDK_TEST_PG_ADMIN and BE_SDK_TEST_NATS_URL are needed")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	t.Cleanup(func() { admin.Close(ctx) })
	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+itSchema+" CASCADE; CREATE SCHEMA "+itSchema)
	require.NoError(t, err)
	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	pw, _ := u.User.Password()
	w := sectest.New(t)
	w.GrantAll(sectest.Alice, core.Definitions())
	w.GrantAll(sectest.Bob, core.Definitions())
	// two legal entities, Shanghai (code SH, CNY) and Beijing (code BJ, CNY), two departments of Shanghai, a customer,
	// a supplier and a suspended partner
	fx := &fixture{t: t, ctx: ctx, w: w, admin: admin, le: ids.New().String(), le2: ids.New().String(), d1: ids.New().String(),
		d2: ids.New().String(), customer: ids.New().String(), supplier: ids.New().String(), blocked: ids.New().String()}
	fx.org = &orgStandIn{les: map[string]map[string]any{fx.le: {"id": fx.le, "code": "SH", "state": "active",
		"timezone": "Asia/Shanghai", "base_currency": "CNY", "version": int64(1)},
		fx.le2: {"id": fx.le2, "code": "BJ", "state": "active", "timezone": "Asia/Shanghai", "base_currency": "CNY",
			"version": int64(1)}},
		units: map[string]map[string]any{fx.d1: fx.unit(fx.d1, fx.le+"/"+fx.d1, 1), fx.d2: fx.unit(fx.d2, fx.le+"/"+fx.d2, 1)}}
	portFree(t, itPort, itSystem, itOrg, itPartner, itCurrency, itPurchase)
	serve(t, itOrg, w, core.Org, func(s *grpc.Server) { orgv3.RegisterSourceServiceServer(s, fx.org) })
	serve(t, itPartner, w, core.Partner, func(s *grpc.Server) {
		p := &partnerStandIn{names: map[string]string{fx.customer: "上海客户", fx.supplier: "上海供应商", fx.blocked: "暂停的伙伴"},
			blocked: map[string]bool{fx.blocked: true}}
		partnerv3.RegisterSourceServiceServer(s, p)
		partnerv3.RegisterPartnerServiceServer(s, p)
	})
	serve(t, itCurrency, w, core.Currency, func(s *grpc.Server) { currencyv3.RegisterSourceServiceServer(s, currencyStandIn{}) })
	fx.purchase = &purchaseStandIn{lines: map[string]*purchasev3.BasisLine{}}
	serve(t, itPurchase, w, core.Purchase, func(s *grpc.Server) { purchasev3.RegisterMatchServiceServer(s, fx.purchase) })

	// a clean stream each run (earlier runs and cells leave events on the shared NATS)
	js, err := jetstream.Connect(natsURL, "finance-it-clean")
	require.NoError(t, err)
	for _, s := range []string{orgStream.Name, core.BillingStream, paymentStream.Name, core.Stream} {
		_ = js.DeleteStream(ctx, s) // not there: nothing to clean
	}
	_ = js.Close()
	fx.tr, err = bus.Open(ctx, natsURL, "finance-it-org", slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { fx.tr.Close() })
	require.NoError(t, fx.tr.EnsureStream(ctx, orgStream))

	env := w.Env(t, core.ID)
	for k, v := range map[string]string{"COMPONENT_ID": core.ID, "COMPONENT_VERSION": finance.Version, "PORT": itPort,
		"STOP_DRAIN_DELAY": "50ms", "PG_HOST": u.Hostname(), "PG_PORT": u.Port(), "PG_DATABASE": strings.TrimPrefix(u.Path, "/"),
		"PG_USER": u.User.Username(), "PG_PASSWORD": pw, "PG_SCHEMA": itSchema, "PG_OWNER_USER": u.User.Username(),
		"PG_OWNER_PASSWORD": pw, "BUS_URL": natsURL, "IMPLEMENTATION_INSTANCE": ids.New().String(),
		"MDM_ORG_GRPC_ENDPOINT": "http://" + itOrg, "MDM_PARTNER_GRPC_ENDPOINT": "http://" + itPartner,
		"MDM_CURRENCY_GRPC_ENDPOINT": "http://" + itCurrency, "ERP_PURCHASE_GRPC_ENDPOINT": "http://" + itPurchase,
		// Shanghai takes 5 % more quantity than invoiceable, no price difference; Beijing declares nothing (0)
		// erp/payment's stand-in publishes fund facts and accounting facts on its stream (ST01)
		"FINANCE_FACT_SOURCES": `[{"stream": "erp.payment", "type": "erp.payment.fund_fact.v1"},
			{"stream": "erp.payment", "type": "erp.payment.accounting_fact.v1"}]`,
		// a pending claim expires after 4 s here (CL05), 7 days by default
		"CLAIM_PERIOD": "4s",
		"MATCH_TOLERANCES": `{"` + fx.le + `": {"quantity_percent": "5", "price_percent": "0"}}`} {
		env[k] = v
	}
	var logs bytes.Buffer
	var logMu sync.Mutex
	t.Cleanup(func() {
		if t.Failed() {
			logMu.Lock()
			t.Log(logs.String())
			logMu.Unlock()
		}
	})
	out := &lockedWriter{w: &logs, mu: &logMu}
	opts := launcher.Options{Stdout: out, Stderr: out,
		Env: config.Env{LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }, Read: os.ReadFile}}
	migrate := opts
	migrate.Args = []string{"migrate"}
	def := finance.Definition()
	newModule := def.New
	def.New = func(rt *runtime.Runtime) (component.Module, error) { fx.rt = rt; return newModule(rt) }
	require.Equal(t, launcher.ExitOK, launcher.Run(ctx, def, migrate))
	sctx, stop := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { done <- launcher.Run(sctx, def, opts) }()
	t.Cleanup(func() { stop(); <-done })
	require.Eventually(t, func() bool {
		r, err := http.Get(itBase + "/readyz")
		if err == nil {
			r.Body.Close()
		}
		return err == nil && r.StatusCode == 200
	}, 20*time.Second, 100*time.Millisecond)
	fx.alice = client{t, w.Bearer(sectest.Alice)}
	fx.bob = client{t, w.Bearer(sectest.Bob)}
	return fx
}

var orgStream = events.Stream{Name: core.OrgStream, Partitions: 4}

var shanghai, _ = time.LoadLocation("Asia/Shanghai")

// today is Shanghai's business date.
func (fx *fixture) today() string { return time.Now().In(shanghai).Format("2006-01-02") }

func (fx *fixture) with(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{k: v}
	for kk, vv := range m {
		if kk != k {
			out[kk] = vv
		}
	}
	return out
}

// fact reads the payload of the incremental fact with the ID from the outbox.
func (fx *fixture) fact(id, typ string) map[string]any {
	return fx.factWhere(typ, "fact_id", id)
}

func (fx *fixture) factWhere(typ, key, value string) map[string]any {
	fx.t.Helper()
	var payload []byte
	require.NoError(fx.t, fx.admin.QueryRow(fx.ctx, "SELECT payload FROM "+itSchema+".be_outbox WHERE type = $1 AND "+
		"convert_from(payload, 'UTF8')::jsonb->>'"+key+"' = $2", typ, value).Scan(&payload), "%s with %s %s", typ, key, value)
	var out map[string]any
	require.NoError(fx.t, json.Unmarshal(payload, &out))
	return out
}

// --- helpers ---

type client struct {
	t     *testing.T
	token string
}

// do calls the user surface; a POST carries a new Idempotency-Key unless key gives one.
func (c client) do(method, path string, body any, key ...string) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, itBase+path, r)
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	if method == "POST" {
		k := ids.New().String()
		if len(key) > 0 {
			k = key[0]
		}
		req.Header.Set("Idempotency-Key", k)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func serve(t *testing.T, addr string, w *sectest.World, as string, register func(*grpc.Server)) {
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(w.TLS(t, as).ServerConfig())))
	register(srv)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
}

// at reads a value along a path of keys and indexes from a decoded JSON object.
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func (fx *fixture) unit(id, path string, version int64) map[string]any {
	return map[string]any{"id": id, "legal_entity_id": fx.le, "parent_id": nil, "code": "D" + id[:4], "name": "部门",
		"path": path, "state": "active", "version": version, "updated_at": "2026-10-01T00:00:00.000000Z"}
}

func (fx *fixture) get(path string) map[string]any {
	fx.t.Helper()
	code, out := fx.alice.do("GET", path, nil)
	require.Equal(fx.t, 200, code, out)
	return out
}

// orgEpoch is the stand-in mdm/org's epoch as its stamp says it (a full recovery advances it).
func (fx *fixture) orgEpoch() events.Epoch {
	fx.org.mu.Lock()
	defer fx.org.mu.Unlock()
	e, err := events.ParseEpoch(orgStamp.GetEpoch())
	require.NoError(fx.t, err)
	return e
}

// publishOrgUnit publishes mdm.org_unit.changed.v1 as mdm/org's forwarder would.
func (fx *fixture) publishOrgUnit(unit map[string]any) {
	data, _ := json.Marshal(unit)
	fx.publishOrg(core.OrgUnitChanged, unit["id"].(string), unit["version"].(int64), events.Full, data)
}

// publishOrg publishes any event on mdm/org's stream, as a later mdm/org with more event types would.
func (fx *fixture) publishOrg(typ, id string, version int64, kind events.Projection, data []byte) {
	row := events.Row{EventID: ids.New().String(), Stream: orgStream.Name, Partition: orgStream.PartitionOf(id),
		Type: typ, OccurredAt: time.Now(), RecordID: id, Version: version,
		Projection: kind, Payload: data, Seq: time.Now().UnixNano(), Epoch: fx.orgEpoch()}
	row.Digest = events.Digest(row.EventID, data)
	require.NoError(fx.t, fx.tr.Publish(fx.ctx, events.Message{Subject: events.Subject(orgStream.Name, row.Partition, row.Type),
		Headers: row.Headers(core.Org, orgStamp.GetInstance()), Data: data}))
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// portFree fails at once when a port the member needs is taken: a running cell or another member would answer in
// its place, and the test would assert against another process.
func portFree(t *testing.T, addrs ...string) {
	for _, a := range addrs {
		if !strings.Contains(a, ":") {
			a = "localhost:" + a
		}
		ln, err := net.Listen("tcp", a)
		require.NoError(t, err, fmt.Sprintf("port %s is taken: is a cell or another member running?", a))
		_ = ln.Close()
	}
}
