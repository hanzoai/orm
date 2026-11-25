// Copyright © 2026 Hanzo AI. MIT License.

package schema_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/orm/schema"
	"github.com/hanzoai/orm/tenant"
	_ "github.com/hanzoai/sqlite"
)

func TestGlobalRegistryCompletenessAndDeterminism(t *testing.T) {
	reg1, err := schema.GlobalRegistry()
	if err != nil {
		t.Fatalf("GlobalRegistry() failed: %v", err)
	}
	reg2 := schema.MustGlobalRegistry()

	if reg1.Fingerprint() != reg2.Fingerprint() {
		t.Fatalf("non-deterministic fingerprint: %s != %s", reg1.Fingerprint(), reg2.Fingerprint())
	}

	wantEntities := []string{
		"Organization",
		"User",
		"OrgBalance",
		"Subscription",
		"Invoice",
		"CommerceLedgerEntry",
		"AIUsage",
		"ComputeSample",
		"ProductEvent",
		"O11ySpan",
	}
	entities := reg1.Entities()
	if len(entities) != len(wantEntities) {
		t.Fatalf("entities count = %d, want %d", len(entities), len(wantEntities))
	}
	for i, name := range wantEntities {
		if entities[i].Name != name {
			t.Errorf("entity[%d] = %q, want %q", i, entities[i].Name, name)
		}
	}

	snaps := reg1.Snapshots()
	if len(snaps) != 2 {
		t.Fatalf("snapshots count = %d, want 2", len(snaps))
	}
	if snaps[0].Name != "snap_global_overview" || snaps[1].Name != "snap_org_health" {
		t.Errorf("unexpected snapshots: %+v", snaps)
	}
}

func TestCompileSQLPostgresAndSQLiteAndTenantBridge(t *testing.T) {
	reg := schema.MustGlobalRegistry()

	pg, err := reg.CompileSQL(schema.DialectPostgres)
	if err != nil {
		t.Fatalf("CompileSQL(postgres) failed: %v", err)
	}
	if len(pg.Tables) != 10 {
		t.Fatalf("postgres tables = %d, want 10", len(pg.Tables))
	}
	if !slices.Contains(pg.CreateSchemas, `CREATE SCHEMA IF NOT EXISTS "commerce"`) {
		t.Errorf("missing commerce schema DDL: %v", pg.CreateSchemas)
	}

	rendered := pg.Render()
	for _, want := range []string{
		`CREATE TABLE IF NOT EXISTS "commerce"."org_balances"`,
		`"org" TEXT NOT NULL CHECK ("org" <> '')`,
		`"cash_minor" BIGINT NOT NULL DEFAULT 0`,
		`"updated_at" TIMESTAMPTZ NOT NULL`,
		`PRIMARY KEY ("org", "currency")`,
		`ALTER TABLE "commerce"."org_balances" ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE "commerce"."org_balances" FORCE ROW LEVEL SECURITY`,
		`"payload_json" JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`"paid_at" TIMESTAMPTZ`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("postgres DDL missing expected fragment:\n  want: %s\n  got:\n%s", want, rendered)
		}
	}

	// Verify SQLite dialect also compiles cleanly with SQLite-safe types/defaults.
	sq, err := reg.CompileSQL(schema.DialectSQLite)
	if err != nil {
		t.Fatalf("CompileSQL(sqlite) failed: %v", err)
	}
	sqSQL := sq.Render()
	if strings.Contains(sqSQL, "ROW LEVEL SECURITY") {
		t.Errorf("sqlite DDL must not contain PostgreSQL RLS clauses")
	}
	if strings.Contains(sqSQL, "TIMESTAMPTZ") || strings.Contains(sqSQL, "JSONB") {
		t.Errorf("sqlite DDL must not contain Postgres-only types TIMESTAMPTZ/JSONB")
	}

	// Verify ToTenantTable bridge on tenant-isolated entity.
	bal, ok := reg.Entity("OrgBalance")
	if !ok {
		t.Fatal("OrgBalance missing")
	}
	tt, err := bal.ToTenantTable()
	if err != nil {
		t.Fatalf("ToTenantTable(OrgBalance) failed: %v", err)
	}
	if tt.Name != "org_balances" {
		t.Errorf("tenant table name = %q, want org_balances", tt.Name)
	}
	if !slices.Equal(tt.Key, []string{"currency"}) {
		t.Errorf("tenant table key = %v, want [currency]", tt.Key)
	}
	for _, f := range tt.Fields {
		if f.Name == "org" {
			t.Errorf("tenant.Table must omit leading tenant column; found %q", f.Name)
		}
	}
}

func TestCompileDatastoreClickHouse262AndInvariants(t *testing.T) {
	reg := schema.MustGlobalRegistry()
	ds, err := reg.CompileDatastore()
	if err != nil {
		t.Fatalf("CompileDatastore() failed: %v", err)
	}

	if len(ds.Databases) != 3 { // analytics, hanzo, event
		t.Fatalf("databases = %d, want 3 (%+v)", len(ds.Databases), ds.Databases)
	}
	if len(ds.Tables) != 10 {
		t.Fatalf("L0 tables = %d, want 10", len(ds.Tables))
	}
	if len(ds.RollupTables) != 6 {
		t.Fatalf("L1 rollup tables = %d, want 6", len(ds.RollupTables))
	}
	if len(ds.MaterializedViews) != 6 {
		t.Fatalf("L1 materialized views = %d, want 6", len(ds.MaterializedViews))
	}
	if len(ds.SnapshotTables) != 2 {
		t.Fatalf("L3 snapshot tables = %d, want 2", len(ds.SnapshotTables))
	}
	if len(ds.SnapshotRefreshes) != 2 {
		t.Fatalf("L3 snapshot refreshes = %d, want 2", len(ds.SnapshotRefreshes))
	}

	// Engine verification per layer.
	engines := map[string]string{}
	for _, tbl := range ds.Tables {
		engines[tbl.Name] = tbl.Engine
	}
	if engines["analytics.dim_orgs"] != "ReplacingMergeTree(updated_at)" {
		t.Errorf("dim_orgs engine = %q", engines["analytics.dim_orgs"])
	}
	if engines["analytics.state_org_balances"] != "ReplacingMergeTree(version)" {
		t.Errorf("state_org_balances engine = %q", engines["analytics.state_org_balances"])
	}
	if engines["analytics.fact_commerce_ledger"] != "MergeTree()" {
		t.Errorf("fact_commerce_ledger engine = %q", engines["analytics.fact_commerce_ledger"])
	}
	if engines["hanzo.cloud_usage"] != "MergeTree()" {
		t.Errorf("hanzo.cloud_usage engine = %q", engines["hanzo.cloud_usage"])
	}
	for _, snap := range ds.SnapshotTables {
		if snap.Engine != "ReplacingMergeTree(refreshed_at)" {
			t.Errorf("snapshot %s engine = %q, want ReplacingMergeTree(refreshed_at)", snap.Name, snap.Engine)
		}
		if !strings.Contains(snap.SQL, "refreshed_at DateTime64(3)") {
			t.Errorf("snapshot %s missing refreshed_at column:\n%s", snap.Name, snap.SQL)
		}
	}

	// Verify every MV and Snapshot refresh passes ClickHouse 26.2 zero-shadowing check
	// and never feeds a ReplacingMergeTree into a rollup.
	for _, mv := range ds.MaterializedViews {
		srcFull := mv.SourceDB + "." + mv.SourceTable
		if strings.HasPrefix(engines[srcFull], "ReplacingMergeTree") {
			t.Errorf("HIP-1405 violation: MV %s reads from ReplacingMergeTree source %s", mv.Name, srcFull)
		}
		if err := schema.CheckClickHouse262Safety(mv.SQL); err != nil {
			t.Errorf("MV %s failed ClickHouse 26.2 check: %v\n%s", mv.Name, err, mv.SQL)
		}
	}
	for _, ref := range ds.SnapshotRefreshes {
		if err := schema.CheckClickHouse262Safety(ref.SQL); err != nil {
			t.Errorf("snapshot refresh %s failed ClickHouse 26.2 check: %v\n%s", ref.Name, err, ref.SQL)
		}
	}

	// Verify ZERO Float32/Float64 columns on all financial entities.
	reFloat := regexp.MustCompile(`\bFloat(32|64)\b`)
	for _, e := range reg.Entities() {
		if !e.Catalog.Financial {
			continue
		}
		tbl, err := schema.CompileDatastoreTable(e)
		if err != nil {
			t.Fatalf("CompileDatastoreTable(%s): %v", e.Name, err)
		}
		if reFloat.MatchString(tbl.SQL) {
			t.Errorf("financial entity %s emitted Float column:\n%s", e.Name, tbl.SQL)
		}
	}

	// Verify DatastoreBundle.Apply topological order and external source gating.
	var executed []string
	err = ds.Apply(
		context.Background(),
		func(_ context.Context, stmt string, _ ...any) error {
			executed = append(executed, stmt)
			return nil
		},
		func(_ context.Context, db, tbl string) (bool, error) {
			// Pretend event.span does not exist yet -> its MV must be skipped cleanly.
			return !(db == "event" && tbl == "span"), nil
		},
	)
	if err != nil {
		t.Fatalf("Apply() failed: %v", err)
	}
	// 3 DBs + 10 L0 tables + 6 L1 rollup tables + 5 MVs (o11y skipped) + 2 L3 snapshots = 26
	if len(executed) != 26 {
		t.Fatalf("Apply executed %d statements, want 26", len(executed))
	}
	if !strings.HasPrefix(executed[0], "CREATE DATABASE IF NOT EXISTS") {
		t.Errorf("first Apply statement must create database, got: %s", executed[0])
	}
}

func TestZAPRoundTripAndDualDialects(t *testing.T) {
	reg := schema.MustGlobalRegistry()

	zapIDL, err := reg.EmitZAP()
	if err != nil {
		t.Fatalf("EmitZAP() failed: %v", err)
	}
	if !strings.Contains(zapIDL, "package hanzo.schema") {
		t.Errorf("ZAP IDL missing package header:\n%s", zapIDL)
	}
	if !strings.Contains(zapIDL, "struct OrgBalance {") {
		t.Errorf("ZAP IDL missing struct OrgBalance")
	}

	// Lossless round-trip: Go structs -> Entity -> .zap -> ParseZAP -> SQL + ClickHouse DDL!
	parsed, err := schema.ParseZAP(zapIDL)
	if err != nil {
		t.Fatalf("ParseZAP() failed: %v\nIDL:\n%s", err, zapIDL)
	}
	if len(parsed.Entities()) != len(reg.Entities()) {
		t.Fatalf("round-trip entities = %d, want %d", len(parsed.Entities()), len(reg.Entities()))
	}

	origSQL, err := reg.CompileSQL(schema.DialectPostgres)
	if err != nil {
		t.Fatalf("orig CompileSQL: %v", err)
	}
	rtSQL, err := parsed.CompileSQL(schema.DialectPostgres)
	if err != nil {
		t.Fatalf("round-trip CompileSQL: %v", err)
	}
	if origSQL.Render() != rtSQL.Render() {
		t.Errorf("SQL DDL diverged after .zap round-trip!\n--- ORIG ---\n%s\n--- ROUNDTRIP ---\n%s", origSQL.Render(), rtSQL.Render())
	}

	origDS, err := reg.CompileDatastore()
	if err != nil {
		t.Fatalf("orig CompileDatastore: %v", err)
	}
	rtDS, err := parsed.CompileDatastore()
	if err != nil {
		t.Fatalf("round-trip CompileDatastore: %v", err)
	}
	if len(origDS.Tables) != len(rtDS.Tables) {
		t.Fatalf("round-trip datastore tables = %d, want %d", len(rtDS.Tables), len(origDS.Tables))
	}
	for i := range origDS.Tables {
		if origDS.Tables[i].SQL != rtDS.Tables[i].SQL {
			t.Errorf("datastore table %s diverged after .zap round-trip:\n--- ORIG ---\n%s\n--- ROUNDTRIP ---\n%s",
				origDS.Tables[i].Name, origDS.Tables[i].SQL, rtDS.Tables[i].SQL)
		}
	}

	// Verify Rust zapc-rs dialect output.
	capnp, err := reg.EmitZAPCapnp()
	if err != nil {
		t.Fatalf("EmitZAPCapnp() failed: %v", err)
	}
	if !strings.HasPrefix(capnp, "@0x") {
		t.Errorf("zapc-rs IDL missing @0x file ID:\n%s", capnp)
	}
	for _, want := range []string{
		"struct OrgBalance {",
		"org @0 :Text;",
		"cashMinor @3 :Int64;",
		"version @7 :UInt64;",
	} {
		if !strings.Contains(capnp, want) {
			t.Errorf("zapc-rs IDL missing %q:\n%s", want, capnp)
		}
	}

	// Verify ZAP-first workflow: raw zapc-rs .zap file without any annotations
	// compiles directly into SQL DDL + ClickHouse DDL + Catalog!
	rawZAP := `@0xc0a8b5e0e5f0a001;
struct WebhookDelivery {
    deliveryId @0 :Text;
    org        @1 :Text;
    statusCode @2 :UInt16;
    latencyMs  @3 :UInt32;
    payload    @4 :Data;
}
`
	zapFirst, err := schema.ParseZAP(rawZAP)
	if err != nil {
		t.Fatalf("ParseZAP(raw zapc-rs) failed: %v", err)
	}
	zfSQL, err := zapFirst.CompileSQL(schema.DialectPostgres)
	if err != nil {
		t.Fatalf("zap-first CompileSQL failed: %v", err)
	}
	if !strings.Contains(zfSQL.Render(), `CREATE TABLE IF NOT EXISTS "webhook_delivery"`) {
		t.Errorf("unexpected zap-first SQL:\n%s", zfSQL.Render())
	}
	zfDS, err := zapFirst.CompileDatastore()
	if err != nil {
		t.Fatalf("zap-first CompileDatastore failed: %v", err)
	}
	if !strings.Contains(zfDS.Render(), `CREATE TABLE IF NOT EXISTS analytics.fact_webhook_delivery`) {
		t.Errorf("unexpected zap-first Datastore DDL:\n%s", zfDS.Render())
	}
}

func TestCatalogAndOpenAPIProjection(t *testing.T) {
	reg := schema.MustGlobalRegistry()

	cat, err := reg.CompileCatalog()
	if err != nil {
		t.Fatalf("CompileCatalog() failed: %v", err)
	}
	if len(cat.Entities) != 10 {
		t.Fatalf("catalog entities = %d, want 10", len(cat.Entities))
	}
	if len(cat.Metrics) < 10 {
		t.Fatalf("catalog metrics = %d, want >= 10", len(cat.Metrics))
	}
	if len(cat.Snapshots) != 2 {
		t.Fatalf("catalog snapshots = %d, want 2", len(cat.Snapshots))
	}

	// Verify PII tagging propagated on User.email.
	foundUserPII := false
	for _, eg := range cat.Entities {
		if eg.Entity == "User" {
			if slices.Contains(eg.PIIFields, "email") {
				foundUserPII = true
			}
		}
	}
	if !foundUserPII {
		t.Errorf("expected User.email to be flagged in catalog PIIFields")
	}

	// Verify pretty JSON marshals cleanly.
	raw, err := cat.PrettyJSON()
	if err != nil {
		t.Fatalf("PrettyJSON failed: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("PrettyJSON produced invalid JSON")
	}

	// Verify OpenAPI 3.1 / MCP components.schemas projection.
	components, err := reg.EmitOpenAPIComponents()
	if err != nil {
		t.Fatalf("EmitOpenAPIComponents failed: %v", err)
	}
	if len(components) != 10 {
		t.Fatalf("OpenAPI components = %d, want 10", len(components))
	}
	balSchema, ok := components["OrgBalance"].(map[string]any)
	if !ok {
		t.Fatalf("missing OrgBalance OpenAPI schema")
	}
	if balSchema["x-hanzo-tenant-key"] != "org" {
		t.Errorf("x-hanzo-tenant-key = %v, want org", balSchema["x-hanzo-tenant-key"])
	}
	if balSchema["x-hanzo-datastore-table"] != "analytics.state_org_balances" {
		t.Errorf("x-hanzo-datastore-table = %v", balSchema["x-hanzo-datastore-table"])
	}
}

func TestSafetyInvariantsRejectInvalidSchemas(t *testing.T) {
	// 1. Financial entity with Float64 MUST fail validation.
	type BadLedger struct {
		ID     string  `schema:"pk,order=1"`
		Org    string  `schema:"tenant,order=2"`
		Amount float64 `schema:"default=0"`
	}
	badReg := schema.NewRegistry("test")
	_, err := schema.RegisterStruct[BadLedger](badReg,
		schema.WithKind(schema.KindFact),
		schema.WithCatalog(schema.CatalogMeta{Domain: "finance", Financial: true}),
	)
	if err == nil || !strings.Contains(err.Error(), "MUST NOT use floating-point") {
		t.Errorf("expected float-money rejection, got: %v", err)
	}

	// 2. Mutable KindState (ReplacingMergeTree) feeding SummingMergeTree MV MUST fail (HIP-1405 §4.1).
	type MutableBalance struct {
		Org       string    `schema:"tenant,pk,order=1"`
		CashMinor int64     `schema:"money=minor"`
		Version   uint64    `schema:"version"`
		UpdatedAt time.Time `schema:"timestamp"`
	}
	_, err = schema.RegisterStruct[MutableBalance](schema.NewRegistry("test"),
		schema.WithKind(schema.KindState),
		schema.WithRollups(schema.RollupSpec{
			Name:       "bad_rollup",
			Database:   "analytics",
			Engine:     schema.EngineSummingMergeTree,
			TimeSource: "updated_at",
			TimeTarget: "day",
			Bucket:     schema.BucketDay,
			Dimensions: []schema.RollupDim{{TargetCol: "org", Type: schema.TypeLowCardString}},
			Measures:   []schema.RollupMeasure{{TargetCol: "cash_sum", ValueType: "Int64", StateExpr: "sum(cash_minor)"}},
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "HIP-1405 invariant violation") {
		t.Errorf("expected HIP-1405 ReplacingMergeTree->MV rejection, got: %v", err)
	}

	// 3. ClickHouse 26.2 alias shadowing in GROUP BY MUST fail.
	badGroupByShadow := `SELECT
		toStartOfHour(ts) AS hour,
		toLowCardinality(org) AS org,
		countState() AS reqs
	FROM hanzo.cloud_usage
	GROUP BY hour, org`
	if err := schema.CheckClickHouse262Safety(badGroupByShadow); err == nil {
		t.Errorf("expected ClickHouse 26.2 GROUP BY alias-shadowing error, got nil")
	}

	// 4. ClickHouse 26.2 sibling aggregate alias shadowing MUST fail.
	badSiblingShadow := `SELECT
		provider,
		sumMerge(billed_nano) AS billed_nano,
		sumMerge(margin_nano) AS margin_nano,
		if(billed_nano > 0, intDiv(margin_nano * 10000, billed_nano), 0) AS margin_bps
	FROM analytics.rollup_ai_daily
	GROUP BY provider`
	if err := schema.CheckClickHouse262Safety(badSiblingShadow); err == nil {
		t.Errorf("expected ClickHouse 26.2 sibling alias-shadowing error, got nil")
	}

	// 5. L3 snapshot scanning raw event.fact MUST fail.
	badSnap := schema.SnapshotSpec{
		Name:             "snap_bad",
		Database:         "analytics",
		Fields:           []schema.Field{{Name: "org", Type: schema.TypeString}, {Name: "events", Type: schema.TypeUint64}},
		OrderBy:          []string{"org"},
		RefreshSelectSQL: "SELECT org, count() AS events, now64(3) AS refreshed_at FROM event.fact GROUP BY org",
	}
	if err := badSnap.Validate(); err == nil || !strings.Contains(err.Error(), "MUST NOT scan raw table") {
		t.Errorf("expected L3 raw-table scan rejection, got: %v", err)
	}

	// 6. RollupDim self-shadowing expression MUST fail.
	type GoodFact struct {
		ID  string    `schema:"pk,order=2"`
		Org string    `schema:"tenant,order=1"`
		TS  time.Time `schema:"timestamp"`
	}
	_, err = schema.RegisterStruct[GoodFact](schema.NewRegistry("test"),
		schema.WithKind(schema.KindFact),
		schema.WithRollups(schema.RollupSpec{
			Name:       "bad_dim_shadow",
			Database:   "analytics",
			Engine:     schema.EngineAggregatingMergeTree,
			TimeSource: "ts",
			TimeTarget: "hour",
			Bucket:     schema.BucketHour,
			Dimensions: []schema.RollupDim{{TargetCol: "org", SourceExpr: "toLowCardinality(org)", Type: schema.TypeLowCardString}},
			Measures:   []schema.RollupMeasure{{TargetCol: "n", AggFunc: "count", StateExpr: "countState()"}},
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "ClickHouse 26.2 alias-shadowing") {
		t.Errorf("expected RollupDim alias-shadowing rejection, got: %v", err)
	}
}

func TestGoldenDDLOutputs(t *testing.T) {
	reg := schema.MustGlobalRegistry()

	// Golden 1: OrgBalance ClickHouse DDL.
	bal, _ := reg.Entity("OrgBalance")
	balCH, err := schema.CompileDatastoreTable(bal)
	if err != nil {
		t.Fatalf("CompileDatastoreTable(OrgBalance): %v", err)
	}
	wantBalCH := `CREATE TABLE IF NOT EXISTS analytics.state_org_balances (
  org String,
  currency LowCardinality(String),
  books UInt8,
  cash_minor Int64,
  credit_minor Int64,
  limit_minor Int64,
  owed_minor Int64,
  version UInt64,
  updated_at DateTime64(3)
) ENGINE = ReplacingMergeTree(version)
ORDER BY (org, currency)`
	if balCH.SQL != wantBalCH {
		t.Errorf("OrgBalance ClickHouse DDL mismatch:\n--- GOT ---\n%s\n--- WANT ---\n%s", balCH.SQL, wantBalCH)
	}

	// Golden 2: CommerceLedgerEntry L1 Rollup + MV (ClickHouse 26.2-safe).
	ledger, _ := reg.Entity("CommerceLedgerEntry")
	rTbl, rMV, err := schema.CompileRollup(ledger.Rollups[0], ledger)
	if err != nil {
		t.Fatalf("CompileRollup(rollup_commerce_daily): %v", err)
	}
	wantRollupTbl := `CREATE TABLE IF NOT EXISTS analytics.rollup_commerce_daily (
  day Date,
  org LowCardinality(String),
  product LowCardinality(String),
  currency LowCardinality(String),
  kind LowCardinality(String),
  net_amount_minor Int64,
  net_cash_minor Int64,
  net_credit_minor Int64,
  entry_count Int64
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, org, product, currency, kind)`
	if rTbl.SQL != wantRollupTbl {
		t.Errorf("rollup_commerce_daily table mismatch:\n--- GOT ---\n%s\n--- WANT ---\n%s", rTbl.SQL, wantRollupTbl)
	}

	wantRollupMV := `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.rollup_commerce_daily_mv
TO analytics.rollup_commerce_daily AS
SELECT
  toDate(event_ts) AS day,
  org,
  product,
  currency,
  kind,
  sum(toInt64(sign) * amount_minor) AS net_amount_minor,
  sum(toInt64(sign) * cash_minor) AS net_cash_minor,
  sum(toInt64(sign) * credit_minor) AS net_credit_minor,
  sum(toInt64(sign)) AS entry_count
FROM analytics.fact_commerce_ledger
GROUP BY day, org, product, currency, kind`
	if rMV.SQL != wantRollupMV {
		t.Errorf("rollup_commerce_daily_mv mismatch:\n--- GOT ---\n%s\n--- WANT ---\n%s", rMV.SQL, wantRollupMV)
	}
}

func TestExecuteCompiledSQLiteDDLAndTenantOpen(t *testing.T) {
	reg := schema.MustGlobalRegistry()
	sq, err := reg.CompileSQL(schema.DialectSQLite)
	if err != nil {
		t.Fatalf("CompileSQL(sqlite): %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	// Execute every generated CREATE TABLE and CREATE INDEX against real SQLite.
	for _, stmt := range sq.AllStatements() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("sqlite exec failed: %v\nSQL:\n%s", err, stmt)
		}
	}

	// Verify tenant CHECK ("org" <> '') constraint is enforced by SQLite.
	_, err = db.Exec(`INSERT INTO "org_balances" ("org", "currency", "books", "cash_minor", "credit_minor", "limit_minor", "owed_minor", "version", "updated_at") VALUES ('', 'usd', 1, 100, 0, 0, 0, 1, '2026-11-25T00:00:00Z')`)
	if err == nil {
		t.Fatalf("expected CHECK (org <> '') failure for empty org, got nil")
	}

	// Verify valid row inserts cleanly and is idempotent under re-running DDL.
	_, err = db.Exec(`INSERT INTO "org_balances" ("org", "currency", "books", "cash_minor", "credit_minor", "limit_minor", "owed_minor", "version", "updated_at") VALUES ('hanzo', 'usd', 1, 50000, 10000, 0, 0, 1, '2026-11-25T00:00:00Z')`)
	if err != nil {
		t.Fatalf("valid insert failed: %v", err)
	}
	for _, stmt := range sq.AllStatements() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("idempotent re-exec failed: %v\nSQL:\n%s", err, stmt)
		}
	}

	// Prove ToTenantTable() boots a real orm/tenant.DB on SQLite without error!
	bal, _ := reg.Entity("OrgBalance")
	balTable, err := bal.ToTenantTable()
	if err != nil {
		t.Fatalf("ToTenantTable: %v", err)
	}
	tdb, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tenant.db"))
	if err != nil {
		t.Fatalf("open tenant sqlite: %v", err)
	}
	defer tdb.Close()

	type orgKey struct{}
	store, err := tenant.Open(context.Background(), tenant.Config{
		Driver: "sqlite",
		DB:     tdb,
		Tables: []tenant.Table{*balTable},
		Tenant: func(ctx context.Context) (string, bool) {
			v, ok := ctx.Value(orgKey{}).(string)
			return v, ok
		},
	})
	if err != nil {
		t.Fatalf("tenant.Open with ToTenantTable() failed: %v", err)
	}
	if store == nil {
		t.Fatal("tenant.Open returned nil store")
	}
}

func TestReflectExistingORMModelCompatibility(t *testing.T) {
	type LegacyPaymentIntent struct {
		orm.Model[LegacyPaymentIntent]
		Org      string         `json:"org"      schema:"tenant,order=1"`
		Amount   int64          `json:"amount"   schema:"money=minor"`
		Currency string         `json:"currency" orm:"default:usd" schema:"lowcard"`
		Metadata map[string]any `json:"metadata" orm:"serialize"`
		Version  uint64         `json:"version"  schema:"version"`
	}

	e, err := schema.Reflect[LegacyPaymentIntent](
		schema.WithKind(schema.KindState),
		schema.WithCatalog(schema.CatalogMeta{Domain: "commerce", Financial: true}),
	)
	if err != nil {
		t.Fatalf("Reflect[LegacyPaymentIntent]: %v", err)
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	sqlDDL, err := schema.CompileSQL(e, schema.DialectPostgres)
	if err != nil {
		t.Fatalf("CompileSQL: %v", err)
	}
	for _, want := range []string{
		`"id" TEXT NOT NULL`,
		`"org" TEXT NOT NULL CHECK ("org" <> '')`,
		`"amount" BIGINT NOT NULL`,
		`"currency" TEXT NOT NULL DEFAULT 'usd'`,
		`"metadata" JSONB NOT NULL`,
		`PRIMARY KEY ("org", "id")`,
	} {
		if !strings.Contains(sqlDDL.CreateTable, want) {
			t.Errorf("legacy orm.Model SQL missing %q:\n%s", want, sqlDDL.CreateTable)
		}
	}

	chDDL, err := schema.CompileDatastoreTable(e)
	if err != nil {
		t.Fatalf("CompileDatastoreTable: %v", err)
	}
	if !strings.Contains(chDDL.SQL, "ENGINE = ReplacingMergeTree(version)") {
		t.Errorf("legacy orm.Model ClickHouse DDL missing ReplacingMergeTree(version):\n%s", chDDL.SQL)
	}
}
