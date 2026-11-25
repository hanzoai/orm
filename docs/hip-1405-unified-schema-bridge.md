# HIP-1405 Addendum: Unified Global Schema Bridge (`hanzoai/orm/schema` ↔ `ZAP` ↔ `zip` → `hanzoai/sql` + `hanzoai/datastore` + `MCP / SDKs`)

> **Merge Target**: Insert as **§2.8 Unified Global Schema Bridge** (and cross-reference in §7.3 Release Qualification Gates) in `HIPs/hip-1405-unified-global-analytics.md`.
> **Canonical Reference Implementation**: `github.com/hanzoai/orm/schema` (branch `feat/unified-global-schema`).

---

### 2.8 Unified Global Schema Bridge — One Declaration, Five Projections

#### Problem: Structural Schema Drift Across Storage, Wire, and API Planes

Prior to the Unified Global Schema Bridge, a single Hanzo domain entity (such as `AIUsage`, `OrgBalance`, or `Subscription`) was manually re-declared in up to five disconnected locations:

1. Transactional ORM struct & PostgreSQL/SQLite migrations (`hanzoai/orm`, `hanzoai/commerce`, `hanzoai/iam`).
2. Duplicated ClickHouse `CREATE TABLE` strings across writer and reader binaries (e.g., `ai/object/cloud_usage.go` vs. `cloud/datastore/cloudusage.go` vs. `cloud/datastore/analytics_schema.go`).
3. Hand-maintained ClickHouse L1 Materialized Views and L3 serving views — vulnerable to ClickHouse 26.2 analyzer alias-shadowing regressions and accidental `ReplacingMergeTree` → `SummingMergeTree` corruption.
4. Separate ZAP wire schemas (`.zap` files for `zapgen` Go/C++/TS/Python and `zapc-rs` Rust).
5. Separate HTTP OpenAPI 3.1 schemas, Unified MCP tool schemas (`hanzoai/zip`), and `analytics.catalog` metadata rows.

Any additive column, rename, money-scale change, or tenant-key update required manual lockstep edits across repositories — making silent drift inevitable.

#### Architecture: Single-Source Schema IR in `github.com/hanzoai/orm/schema`

Every canonical Hanzo entity MUST be declared **ONCE** — either as a typed Go struct with `schema` / `orm` / `json` tags or as a `.zap` IDL file with `# @entity` / `# @field` directives — and compiled deterministically through `github.com/hanzoai/orm/schema` into all downstream targets:

```text
+-----------------------------------------------------------------------------+
|  Single Authoritative Declaration (Choose One — Lossless Bidirectional IR)  |
|                                                                             |
|    Go Structs (`schema` / `orm` / `json` tags)   <===>   ZAP IDL (`.zap`)   |
|    `schema.RegisterStruct[T](reg, ...)`                  `schema.ParseZAP`  |
+-------------------------------------+---------------------------------------+
                                      |
                                      v
+-----------------------------------------------------------------------------+
|            Canonical Schema IR (`github.com/hanzoai/orm/schema`)            |
|                                                                             |
|  Compile-Time & CI Invariant Enforcer (`Registry.Validate()`):              |
|   [x] Zero Float32/Float64 on financial entities (`money=minor|nano` only)  |
|   [x] Zero mutable `ReplacingMergeTree` feeding `Summing`/`Aggregating` MVs |
|   [x] Zero ClickHouse 26.2 alias shadowing (`CheckClickHouse262Safety`)     |
|   [x] Zero raw-event scans in L3 serving snapshots                          |
|   [x] Mandatory non-null tenant column (`org`) leading SQL PK & all indexes |
|   [x] Deterministic SHA-256 `Registry.Fingerprint()` for CI drift gates     |
+--------+------------------+-------------------+-------------------+---------+
         |                  |                   |                   |
         v                  v                   v                   v
+----------------+ +------------------+ +---------------+ +-------------------+
|  hanzoai/sql   | | hanzoai/datastore| |   ZAP Wire    | | zip / MCP / SDKs  |
|  (HIP-0402)    | |   (HIP-0401)     | | (.zap IDL)    | | (HIP-0139/0300)   |
+----------------+ +------------------+ +---------------+ +-------------------+
| • PG/SQLite    | | • L0 Dim/State:  | | • Go/TS/Py:   | | • analytics.      |
|   CREATE TABLE | |   ReplacingMT    | |   zap/go/idl  | |   catalog metrics |
| • TIMESTAMPTZ, | | • L0 Facts:      | |   aligned @N  | | • PII redaction   |
|   BIGINT,JSONB | |   MergeTree+TTL  | | • Rust:       | |   field list      |
| • Tenant CHECK | | • L1 Rollups+MV: | |   zapc-rs     | | • OpenAPI 3.1     |
|   & PG RLS     | |   AggregatingMT/ | |   @0x file ID | |   components +    |
| • Direct       | |   SummingMT      | | • Lossless    | |   x-hanzo-* meta  |
|   orm/tenant.  | | • L3 Snapshots:  | |   round-trip  | | • Unified /v1/mcp |
|   Table bridge | |   ReplacingMT    | |   ParseZAP()  | |   tool schemas    |
|                | |   (refreshed_at) | |               | |                   |
+----------------+ +------------------+ +---------------+ +-------------------+
```

#### Canonical Global Entity Registry (`schema.GlobalRegistry()`)

All core cross-ecosystem entities are defined once in `github.com/hanzoai/orm/schema` (`schema/hanzo.go`) and verified by golden DDL and `.zap` round-trip tests:

| # | Canonical Entity | Kind | `hanzoai/sql` Table | `hanzoai/datastore` Table | ClickHouse Engine | Tenant Key (RLS) | Retention | Attached L1 Rollups |
| :- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| 1 | `Organization` | `dim` | `organizations` | `analytics.dim_orgs` | `ReplacingMergeTree(updated_at)` | — (org PK) | — | — |
| 2 | `User` | `dim` | `users` | `analytics.dim_users` | `ReplacingMergeTree(updated_at)` | `org` (RLS) | — | — |
| 3 | `OrgBalance` | `state` | `commerce.org_balances` | `analytics.state_org_balances` | `ReplacingMergeTree(version)` | `org` (RLS) | — | *(forbidden by §4.1)* |
| 4 | `Subscription` | `state` | `commerce.subscriptions` | `analytics.state_subscriptions` | `ReplacingMergeTree(version)` | `org` (RLS) | — | *(forbidden by §4.1)* |
| 5 | `Invoice` | `state` | `commerce.invoices` | `analytics.state_invoices` | `ReplacingMergeTree(version)` | `org` (RLS) | — | *(forbidden by §4.1)* |
| 6 | `CommerceLedgerEntry` | `fact` | `commerce.commerce_ledger_entries` | `analytics.fact_commerce_ledger` | `MergeTree()` | `org` (RLS) | `7 YEAR` | `analytics.rollup_commerce_daily` (`SummingMergeTree`) |
| 7 | `AIUsage` | `fact` | `ai_usage` | `hanzo.cloud_usage` | `MergeTree()` | `org` | `2 YEAR` | `analytics.rollup_ai_hourly`, `analytics.rollup_ai_daily` (`AggregatingMergeTree`) |
| 8 | `ComputeSample` | `fact` | `compute_samples` | `hanzo.compute_samples` | `MergeTree()` | `org` | `90 DAY` | `analytics.rollup_compute_hourly` (`AggregatingMergeTree`) |
| 9 | `ProductEvent` | `fact` | `product_events` | `event.fact` | `MergeTree()` | `org` | `365 DAY` | `analytics.rollup_product_hourly` (`AggregatingMergeTree`) |
| 10 | `O11ySpan` | `fact` | `o11y_spans` | `event.span` | `MergeTree()` | `org` | `30 DAY` | `analytics.rollup_o11y_hourly` (`AggregatingMergeTree`) |

**Canonical L3 Serving Snapshots** (`ReplacingMergeTree(refreshed_at)`):
- `analytics.snap_global_overview` — O(1) fleet-wide executive KPI snapshot (`ORDER BY (scope)`), refreshed exclusively from `dim_orgs`, `state_org_balances`, `state_subscriptions`, `rollup_ai_daily`, and `rollup_o11y_hourly`.
- `analytics.snap_org_health` — per-tenant organization health & economics snapshot (`ORDER BY (org)`), refreshed exclusively from L0 state/dims and L1 rollups.

#### ClickHouse 26.2 Zero-Alias-Shadowing Guarantees

ClickHouse 26.2 enables the new SQL analyzer by default, which rejects or misresolves queries that transform or aggregate a column and alias it back to the exact same identifier when referenced in `GROUP BY` or sibling `SELECT` expressions (e.g., `toLowCardinality(org) AS org ... GROUP BY org` or `sumMerge(billed_nano) AS billed_nano, if(billed_nano > 0, ...)`).

The schema compiler prevents this class of production regression structurally:
1. **By construction**: `CompileRollup` enforces `TimeTarget != TimeSource`, emits bare dimension columns when `SourceCol == TargetCol`, and uses distinct non-shadowing aggregate measure column names (`net_amount_minor`, `billed_nano_sum`, `latest_cash_minor`).
2. **By static verification**: `schema.CheckClickHouse262Safety(sql)` parses every compiled Materialized View and L3 snapshot `RefreshSelectSQL` (including nested subqueries) and fails compilation/CI on any self-shadowing `GROUP BY` or sibling `SELECT` alias reuse.

#### Closing the Loop Across Services & SDKs (Implementation Blueprint)

1. **`hanzoai/orm/schema` (Canonical Source — Landed)**:
   - Owns the IR (`Entity`, `Field`, `RollupSpec`, `SnapshotSpec`, `CatalogMeta`), struct reflector (`Reflect[T]`, `RegisterStruct[T]`), bidirectional `.zap` compiler/parser (`EmitZAP`, `EmitZAPCapnp`, `ParseZAP`), SQL compiler (`CompileSQL` + `ToTenantTable()`), ClickHouse 26.2 compiler (`CompileDatastore` + `DatastoreBundle.Apply()`), and OpenAPI 3.1 / Catalog projector (`CompileCatalog`, `EmitOpenAPIComponents`).
2. **`hanzoai/cloud` & `hanzoai/ai` (Zero Duplicated DDL)**:
   - Replace hand-copied DDL constants in `cloud/datastore/cloudusage.go`, `ai/object/cloud_usage.go`, and `cloud/datastore/analytics_schema.go` with `ds, _ := schema.MustGlobalRegistry().CompileDatastore(); ds.Apply(ctx, datastore.Exec, tableExists)`.
   - Wire `schema.MustGlobalRegistry().CompileCatalog()` directly into `analytics.catalog` seeding and `/v1/analytics/catalog`.
3. **`hanzoai/zip` (OpenAPI 3.1 + Unified MCP Projection)**:
   - Merge `schema.MustGlobalRegistry().EmitOpenAPIComponents()` into `zip`'s `/.well-known/openapi.json` and `/v1/mcp` tool schemas so REST, OpenAPI, and MCP agents share exact field names, integer money units (`x-hanzo-unit`), tenant keys (`x-hanzo-tenant-key`), and PII redaction lists (`x-hanzo-pii`).
4. **Multi-Language Wire & SDK Codegen (`Go` / `Rust` / `TypeScript` / `Python`)**:
   - CI generates and checks in:
     - `hanzo_global.zap` (`EmitZAP()`) → fed into `zapgen` (`github.com/zap-proto/go/cmd/zapgen`) for Go/TS/Python wire codecs.
     - `hanzo_global_rs.zap` (`EmitZAPCapnp()`) → fed into `zapc-rs` (`hanzoai/zap`) for Rust `zapwire` codecs.
     - `openapi_components.json` (`EmitOpenAPIComponents()`) → fed into TypeScript SDK (`@hanzo/sdk`) and Python SDK (`hanzo`) type generation.
   - CI release gate compares `Registry.Fingerprint()` across `hanzoai/orm`, `hanzoai/cloud`, and generated SDK artifacts — failing merge on any unregenerated schema drift.
