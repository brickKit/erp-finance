// Package module 是 erp-finance 唯一的装配入口。独立运行（cmd/server 的
// besdk.RunStandalone）与进外壳走同一个 New：模块只交回零件（HTTP handler、
// gRPC 注册函数、后台循环），谁去 Listen、谁开连接池、谁初始化 OTel 与信号
// 处理，全归调用方——这样同一份代码进外壳之后不会与别的成员互相覆盖。
package module

import (
	"context"

	besdk "github.com/brickKit/be-sdk-go"
	financev1 "github.com/brickKit/erp-finance/gen/erp/finance/v1"
	"google.golang.org/grpc"

	"github.com/brickKit/erp-finance/v2/backend/internal/consumer"
	grpcapi "github.com/brickKit/erp-finance/v2/backend/internal/grpc"
	httpapi "github.com/brickKit/erp-finance/v2/backend/internal/http"
	"github.com/brickKit/erp-finance/v2/backend/internal/partition"
	"github.com/brickKit/erp-finance/v2/backend/internal/repo"
	"github.com/brickKit/erp-finance/v2/backend/internal/service"
)

// New 构造 erp-finance 模块。签名是外壳与 RunStandalone 共同依赖的约定，
// 不改。
func New(ctx context.Context, rt *besdk.Runtime) (*besdk.Module, error) {
	// 配置只从 rt.Config 读，模块里不碰 os.Getenv：一个进程只有一份环境，
	// 进外壳后各成员的 PG_SCHEMA 会互相覆盖，不报错，模块就按别人的 schema
	// 读写数据。
	schema := rt.Config.StringOr("PG_SCHEMA", "erp_finance")
	// role 是每个事务里 SET LOCAL ROLE 的目标：独立运行时它就是登录角色；
	// 进外壳后外壳以自己的角色登录，靠这一步切到本组件的角色。
	role := schema + "_rw"

	// 连接池、日志都从 rt 来，不自己 sql.Open：进程级的东西"最后一个赢"，
	// 进外壳后会与别的成员互相覆盖。
	r := repo.New(rt.DB, role, schema)
	svc := service.New(r, rt.Logger)

	// HTTP：engine 必须用 besdk.NewGinEngine，它已挂好 OTel / request-id /
	// error→status / PII 脱敏日志 / RED 指标 / /healthz / /metrics。
	eng := besdk.NewGinEngine(rt)
	httpapi.RegisterRoutes(eng, svc)

	return &besdk.Module{
		HTTPHandler: eng,

		// gRPC 由调用方在 extraPorts 的 grpc 端口上 Listen。进外壳后同进程的
		// 别的成员照样经 gRPC 调本组件，不直接调函数：边界在合并时不消失，
		// 组件才能随时拆回独立部署。
		RegisterGRPC: func(gs *grpc.Server) {
			financev1.RegisterFinanceServiceServer(gs, grpcapi.New(svc))
		},

		// 后台循环：Outbox 推送 + event_outbox / event_inbox 的周分区维护 + 事件消费。
		// 三个都阻塞到 ctx 取消才返回，必须并发跑，顺序调用的话后面的永远轮不到。
		Start: func(ctx context.Context) error {
			errCh := make(chan error, 3)
			go func() { errCh <- besdk.StartOutboxPump(ctx, rt.DB, schema, rt.NATS, rt.Logger) }()
			go func() { errCh <- partition.Start(ctx, rt.DB, role, schema, rt.Logger) }()
			go func() { errCh <- consumer.Start(ctx, rt.DB, role, schema, rt.NATS, rt.Logger) }()

			select {
			case <-ctx.Done():
				return nil
			case err := <-errCh:
				return err // 返回 error，不 log.Fatal：进外壳后一个成员退出进程，同进程的成员全部下线
			}
		},
		Stop: func(ctx context.Context) error { return nil }, // 后台循环靠 ctx 退出
	}, nil
}
