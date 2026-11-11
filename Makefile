.PHONY: test test-integration lint seed

test:  ## unit tests: the manifest, the OpenAPI document and the routes, merge safety
	go vet ./... && go test ./...

test-integration:  ## L2 against PostgreSQL and the bus (BE_SDK_TEST_PG_ADMIN, BE_SDK_TEST_NATS_URL)
	go test -count=1 -tags integration ./itest/

lint:
	brickkit lint


seed:  ## the seed data for people, through the running component's user surface (SEED_PASSWORD; see cmd/seed)
	$(MAKE) -C ../../mdm/org seed   # the declared dependencies first; the rest come through them (seed and test data conventions)
	$(MAKE) -C ../../mdm/partner seed
	$(MAKE) -C ../../mdm/currency seed
	$(MAKE) -C ../purchase seed     # its completed orders are invoiced here
	$(MAKE) -C ../billing seed      # its issued invoices form the receivables here
	go run ./cmd/seed
