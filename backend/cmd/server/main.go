package main

import (
	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/erp-finance/v2/backend/module"
)

// 独立运行的入口只有这一行：装配逻辑全在 backend/module，进外壳时外壳调同一个 New。
func main() { besdk.RunStandalone(module.New) }
