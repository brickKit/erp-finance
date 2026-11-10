// Command erp-finance runs the finance component alone (serve, migrate); the shells compile in finance.Definition().
package main

import (
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/erp-finance/v3/finance"
)

func main() { launcher.Main(finance.Definition()) }
