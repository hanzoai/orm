// Copyright © 2026 Hanzo AI. MIT License.

package schema

import (
	"encoding/json"
	"time"
)

// ============================================================================
// Canonical Hanzo Global Entities — Declared ONCE, Compiled Everywhere
//
// Each struct below is the single source of truth across:
//   1. hanzoai/sql       (PostgreSQL & SQLite DDL + indexes + tenant RLS)
//   2. hanzoai/datastore (ClickHouse 26.2 L0 tables + L1 rollups/MVs + L3 snapshots)
//   3. ZAP IDL (.zap)    (Go zapgen + Rust zapc-rs + Python + TypeScript)
//   4. Analytics Catalog (analytics.catalog metrics + OpenAPI 3.1 / MCP schemas)
// ============================================================================

// Organization is the canonical tenant dimension (`organizations` / `analytics.dim_orgs`).
type Organization struct {
	Org         string    `json:"org"          schema:"pk,order=1,comment=Canonical organization slug"`
	DisplayName string    `json:"display_name" schema:"default=''"`
	CreatedAt   time.Time `json:"created_at"   schema:"timestamp"`
	IsInternal  bool      `json:"is_internal"  schema:"default=false"`
	Members     uint64    `json:"members"      schema:"default=0"`
	Tier        string    `json:"tier"         schema:"lowcard,index,default='free'"`
	Status      string    `json:"status"       schema:"lowcard,index,default='active'"`
	UpdatedAt   time.Time `json:"updated_at"   schema:"version"`
}

// User is the canonical IAM user dimension (`users` / `analytics.dim_users`).
type User struct {
	Org          string    `json:"org"            schema:"tenant=rls,pk,order=1"`
	UserID       string    `json:"user_id"        schema:"pk,order=2"`
	Name         string    `json:"name"           schema:"default=''"`
	Email        string    `json:"email"          schema:"index,pii,default=''"`
	CreatedAt    time.Time `json:"created_at"     schema:"timestamp"`
	LastSigninAt time.Time `json:"last_signin_at" schema:"timestamp"`
	IsAdmin      bool      `json:"is_admin"       schema:"default=false"`
	Forbidden    bool      `json:"forbidden"      schema:"default=false"`
	UpdatedAt    time.Time `json:"updated_at"     schema:"version"`
}

// OrgBalance is the point-in-time latest-state commerce balance projection
// (`org_balances` / `analytics.state_org_balances`).
type OrgBalance struct {
	Org         string    `json:"org"          schema:"tenant=rls,pk,order=1"`
	Currency    string    `json:"currency"     schema:"lowcard,pk,order=2,default='usd'"`
	Books       bool      `json:"books"        schema:"default=false"`
	CashMinor   int64     `json:"cash_minor"   schema:"money=minor,default=0"`
	CreditMinor int64     `json:"credit_minor" schema:"money=minor,default=0"`
	LimitMinor  int64     `json:"limit_minor"  schema:"money=minor,default=0"`
	OwedMinor   int64     `json:"owed_minor"   schema:"money=minor,default=0"`
	Version     uint64    `json:"version"      schema:"version,default=0"`
	UpdatedAt   time.Time `json:"updated_at"   schema:"timestamp"`
}

// Subscription is the point-in-time latest-state subscription projection
// (`subscriptions` / `analytics.state_subscriptions`).
type Subscription struct {
	Org               string          `json:"org"                  schema:"tenant=rls,pk,order=1"`
	ID                string          `json:"id"                   schema:"pk,order=2"`
	Deleted           bool            `json:"deleted"              schema:"tombstone,default=false"`
	UserID            string          `json:"user_id"              schema:"index,default=''"`
	PlanID            string          `json:"plan_id"              schema:"index,default=''"`
	PlanName          string          `json:"plan_name"            schema:"default=''"`
	Status            string          `json:"status"               schema:"lowcard,index,default='active'"`
	MRRMinor          int64           `json:"mrr_minor"            schema:"money=minor,default=0"`
	Seats             int64           `json:"seats"                schema:"default=1"`
	Interval          string          `json:"interval"             schema:"lowcard,default='month'"`
	Provider          string          `json:"provider"             schema:"lowcard,default='stripe'"`
	Settled           string          `json:"settled"              schema:"lowcard,default=''"`
	Currency          string          `json:"currency"             schema:"lowcard,default='usd'"`
	Test              bool            `json:"test"                 schema:"default=false"`
	CancelAtPeriodEnd bool            `json:"cancel_at_period_end" schema:"default=false"`
	PayloadJSON       json.RawMessage `json:"payload_json"         schema:"json,default='{}'"`
	Version           uint64          `json:"version"              schema:"version,default=0"`
	UpdatedAt         time.Time       `json:"updated_at"           schema:"timestamp"`
}

// Invoice is the point-in-time latest-state invoice projection
// (`invoices` / `analytics.state_invoices`).
type Invoice struct {
	Org            string          `json:"org"             schema:"tenant=rls,pk,order=1"`
	ID             string          `json:"id"              schema:"pk,order=2"`
	Deleted        bool            `json:"deleted"         schema:"tombstone,default=false"`
	Number         string          `json:"number"          schema:"index,default=''"`
	UserID         string          `json:"user_id"         schema:"index,default=''"`
	SubscriptionID string          `json:"subscription_id" schema:"index,default=''"`
	Status         string          `json:"status"          schema:"lowcard,index,default='open'"`
	AmountMinor    int64           `json:"amount_minor"    schema:"money=minor,default=0"`
	PaidMinor      int64           `json:"paid_minor"      schema:"money=minor,default=0"`
	Currency       string          `json:"currency"        schema:"lowcard,default='usd'"`
	IssuedAt       time.Time       `json:"issued_at"       schema:"timestamp"`
	PaidAt         *time.Time      `json:"paid_at"         schema:"nullable"`
	PayloadJSON    json.RawMessage `json:"payload_json"    schema:"json,default='{}'"`
	Version        uint64          `json:"version"         schema:"version,default=0"`
	UpdatedAt      time.Time       `json:"updated_at"      schema:"timestamp"`
}

// CommerceLedgerEntry is the immutable append-only double-entry financial flow
// fact (`commerce_ledger_entries` / `analytics.fact_commerce_ledger`).
//
// Safe source for incremental SummingMergeTree L1 rollup (`rollup_commerce_daily`).
type CommerceLedgerEntry struct {
	EntryID       string    `json:"entry_id"       schema:"pk,order=4"`
	Org           string    `json:"org"            schema:"lowcard,tenant=rls,order=2"`
	Product       string    `json:"product"        schema:"lowcard,index,default=''"`
	Kind          string    `json:"kind"           schema:"lowcard,index,default='usage'"`
	Sign          int8      `json:"sign"           schema:"sign,default=1"`
	AmountMinor   int64     `json:"amount_minor"   schema:"money=minor,default=0"`
	CashMinor     int64     `json:"cash_minor"     schema:"money=minor,default=0"`
	CreditMinor   int64     `json:"credit_minor"   schema:"money=minor,default=0"`
	Currency      string    `json:"currency"       schema:"lowcard,order=3,default='usd'"`
	FXRateNano    int64     `json:"fx_rate_nano"   schema:"money=nano,default=1000000000"`
	FXAt          time.Time `json:"fx_at"          schema:"timestamp"`
	SourceVersion uint64    `json:"source_version" schema:"default=0"`
	EventTS       time.Time `json:"event_ts"       schema:"timestamp,partition,order=1,index"`
	IngestedAt    time.Time `json:"ingested_at"    schema:"timestamp"`
}

// AIUsage is the immutable per-inference AI gateway metering ledger
// (`ai_usage` / `hanzo.cloud_usage`).
type AIUsage struct {
	ID               string    `json:"id"                schema:"pk,order=4"`
	Timestamp        time.Time `json:"timestamp"         schema:"timestamp,partition,order=1,index"`
	Org              string    `json:"org"               schema:"lowcard,tenant,order=2"`
	Project          string    `json:"project"           schema:"lowcard,default=''"`
	UserID           string    `json:"user_id"           schema:"order=3,index,default=''"`
	Model            string    `json:"model"             schema:"lowcard,index,default=''"`
	Provider         string    `json:"provider"          schema:"lowcard,index,default=''"`
	RequestID        string    `json:"request_id"        schema:"default=''"`
	PromptTokens     uint32    `json:"prompt_tokens"     schema:"unit=tokens,default=0"`
	CompletionTokens uint32    `json:"completion_tokens" schema:"unit=tokens,default=0"`
	TotalTokens      uint32    `json:"total_tokens"      schema:"unit=tokens,default=0"`
	CacheReadTokens  uint32    `json:"cache_read_tokens" schema:"unit=tokens,default=0"`
	CacheWriteTokens uint32    `json:"cache_write_tokens" schema:"unit=tokens,default=0"`
	CostNano         int64     `json:"cost_nano"         schema:"money=nano,default=0"`
	BilledNano       int64     `json:"billed_nano"       schema:"money=nano,default=0"`
	MarginNano       int64     `json:"margin_nano"       schema:"money=nano,default=0"`
	CacheSavedNano   int64     `json:"cache_saved_nano"  schema:"money=nano,default=0"`
	Currency         string    `json:"currency"          schema:"lowcard,default='usd'"`
	Status           string    `json:"status"            schema:"lowcard,default='ok'"`
	BYO              bool      `json:"byo"               schema:"default=false"`
	Unpriced         bool      `json:"unpriced"          schema:"default=false"`
}

// ComputeSample is the immutable GPU/CPU/memory/sandbox utilization & metering
// telemetry sample (`compute_samples` / `hanzo.compute_samples`).
type ComputeSample struct {
	SampleID   string    `json:"sample_id"   schema:"pk,order=4"`
	Timestamp  time.Time `json:"timestamp"   schema:"timestamp,partition,order=1,index"`
	Org        string    `json:"org"         schema:"lowcard,tenant,order=2"`
	Cluster    string    `json:"cluster"     schema:"lowcard,default=''"`
	Region     string    `json:"region"      schema:"lowcard,default=''"`
	NodeID     string    `json:"node_id"     schema:"order=3,index,default=''"`
	WorkloadID string    `json:"workload_id" schema:"index,default=''"`
	GPUModel   string    `json:"gpu_model"   schema:"lowcard,default=''"`
	Tier       string    `json:"tier"        schema:"lowcard,default='dedicated'"`
	CPUMilli   uint64    `json:"cpu_milli"   schema:"unit=millicores,default=0"`
	MemBytes   uint64    `json:"mem_bytes"   schema:"unit=bytes,default=0"`
	GPUCount   uint16    `json:"gpu_count"   schema:"unit=count,default=0"`
	GPUUtilBPS uint16    `json:"gpu_util_bps" schema:"unit=bps,default=0"` // 0..10000 basis points (exact integer)
	DurationMS uint32    `json:"duration_ms" schema:"unit=ms,default=0"`
	CostNano   int64     `json:"cost_nano"   schema:"money=nano,default=0"`
}

// ProductEvent is the immutable behavioral & product analytics event fact
// (`product_events` / `event.fact`).
type ProductEvent struct {
	EventID        string          `json:"event_id"        schema:"pk,order=4"`
	EventTime      time.Time       `json:"event_time"      schema:"timestamp,partition,order=1,index"`
	Org            string          `json:"org"             schema:"lowcard,tenant,order=2"`
	Product        string          `json:"product"         schema:"lowcard,order=3,index"`
	Name           string          `json:"name"            schema:"index"`
	PersonID       string          `json:"person_id"       schema:"default=''"`
	SessionID      string          `json:"session_id"      schema:"default=''"`
	Bytes          uint64          `json:"bytes"           schema:"unit=bytes,default=0"`
	PropertiesJSON json.RawMessage `json:"properties_json" schema:"json,default='{}'"`
}

// O11ySpan is the immutable distributed trace span fact (`o11y_spans` / `event.span`).
type O11ySpan struct {
	SpanID         string          `json:"span_id"         schema:"pk,order=4"`
	TraceID        string          `json:"trace_id"        schema:"index"`
	ParentSpanID   string          `json:"parent_span_id"  schema:"default=''"`
	SpanTime       time.Time       `json:"span_time"       schema:"timestamp,partition,order=1,index"`
	Org            string          `json:"org"             schema:"lowcard,tenant,order=2"`
	Service        string          `json:"service"         schema:"lowcard,order=3,index"`
	Operation      string          `json:"operation"       schema:"default=''"`
	Status         string          `json:"status"          schema:"lowcard,default='ok'"`
	DurationNS     uint64          `json:"duration_ns"     schema:"unit=ns,default=0"`
	AttributesJSON json.RawMessage `json:"attributes_json" schema:"json,default='{}'"`
}

// GlobalRegistry builds and validates the canonical Hanzo global schema
// registry containing all 10 core entities, 6 ClickHouse L1 incremental rollups
// + Materialized Views, and 2 ClickHouse L3 serving snapshots.
func GlobalRegistry() (*Registry, error) {
	reg := NewRegistry("hanzo.schema")

	// 1. Organization (dim_orgs)
	if _, err := RegisterStruct[Organization](reg,
		WithKind(KindDim),
		WithSQL("public", "organizations"),
		WithDatastore("analytics", "dim_orgs"),
		WithCatalog(CatalogMeta{
			Domain:            "iam",
			OwnerTeam:         "platform",
			FreshnessTargetMS: 15000,
			Accuracy:          "exact",
			Description:       "Conformed organization dimension shared across IAM, Commerce, Cloud, and Analytics.",
			Metrics: []MetricSpec{
				{
					Metric:            "iam.orgs_total",
					Domain:            "iam",
					Type:              "gauge",
					Unit:              "count",
					Aggregation:       "count",
					Dimensions:        []string{"tier", "status", "is_internal"},
					ServingTable:      "analytics.snap_global_overview",
					FreshnessTargetMS: 15000,
					Accuracy:          "exact",
					Description:       "Total registered organizations across the Hanzo fleet.",
				},
			},
		}),
	); err != nil {
		return nil, err
	}

	// 2. User (dim_users)
	if _, err := RegisterStruct[User](reg,
		WithKind(KindDim),
		WithSQL("public", "users"),
		WithDatastore("analytics", "dim_users"),
		WithCatalog(CatalogMeta{
			Domain:            "iam",
			OwnerTeam:         "platform",
			FreshnessTargetMS: 15000,
			Accuracy:          "exact",
			Description:       "Conformed IAM user dimension with tenant isolation and PII email redaction.",
			Metrics: []MetricSpec{
				{
					Metric:            "iam.users_total",
					Domain:            "iam",
					Type:              "gauge",
					Unit:              "count",
					Aggregation:       "count",
					Dimensions:        []string{"org", "is_admin", "forbidden"},
					ServingTable:      "analytics.snap_org_health",
					FreshnessTargetMS: 15000,
					Accuracy:          "exact",
					Description:       "Total IAM users per organization.",
				},
			},
		}),
	); err != nil {
		return nil, err
	}

	// 3. OrgBalance (state_org_balances)
	if _, err := RegisterStruct[OrgBalance](reg,
		WithKind(KindState),
		WithSQL("commerce", "org_balances"),
		WithDatastore("analytics", "state_org_balances"),
		WithCatalog(CatalogMeta{
			Domain:            "finance",
			OwnerTeam:         "commerce",
			FreshnessTargetMS: 5000,
			Accuracy:          "exact",
			Financial:         true,
			Description:       "Point-in-time organization cash, promotional credit, credit limit, and owed balances in integer minor units.",
			Metrics: []MetricSpec{
				{
					Metric:            "finance.cash_minor",
					Domain:            "finance",
					Type:              "currency",
					Unit:              "minor_usd",
					Aggregation:       "sum",
					Dimensions:        []string{"org", "currency"},
					ServingTable:      "analytics.snap_global_overview",
					FreshnessTargetMS: 5000,
					Accuracy:          "exact",
					Description:       "Exact cash balance in minor currency units across all org books.",
				},
				{
					Metric:            "finance.credit_minor",
					Domain:            "finance",
					Type:              "currency",
					Unit:              "minor_usd",
					Aggregation:       "sum",
					Dimensions:        []string{"org", "currency"},
					ServingTable:      "analytics.snap_global_overview",
					FreshnessTargetMS: 5000,
					Accuracy:          "exact",
					Description:       "Exact promotional/granted credit balance in minor currency units.",
				},
			},
		}),
	); err != nil {
		return nil, err
	}

	// 4. Subscription (state_subscriptions)
	if _, err := RegisterStruct[Subscription](reg,
		WithKind(KindState),
		WithSQL("commerce", "subscriptions"),
		WithDatastore("analytics", "state_subscriptions"),
		WithCatalog(CatalogMeta{
			Domain:            "commerce",
			OwnerTeam:         "commerce",
			FreshnessTargetMS: 15000,
			Accuracy:          "exact",
			Financial:         true,
			Description:       "Latest-state recurring subscription records with exact minor-unit MRR and soft-delete tombstones.",
			Metrics: []MetricSpec{
				{
					Metric:            "commerce.mrr_minor",
					Domain:            "commerce",
					Type:              "currency",
					Unit:              "minor_usd",
					Aggregation:       "sum",
					Dimensions:        []string{"org", "currency", "plan_id", "provider"},
					ServingTable:      "analytics.snap_global_overview",
					FreshnessTargetMS: 15000,
					Accuracy:          "exact",
					Description:       "Active non-test recurring subscription revenue normalized to monthly minor currency units.",
				},
				{
					Metric:            "commerce.active_subscriptions",
					Domain:            "commerce",
					Type:              "gauge",
					Unit:              "count",
					Aggregation:       "count",
					Dimensions:        []string{"org", "plan_id", "status", "provider"},
					ServingTable:      "analytics.snap_global_overview",
					FreshnessTargetMS: 15000,
					Accuracy:          "exact",
					Description:       "Count of active or trialing non-deleted, non-test subscriptions.",
				},
			},
		}),
	); err != nil {
		return nil, err
	}

	// 5. Invoice (state_invoices)
	if _, err := RegisterStruct[Invoice](reg,
		WithKind(KindState),
		WithSQL("commerce", "invoices"),
		WithDatastore("analytics", "state_invoices"),
		WithCatalog(CatalogMeta{
			Domain:            "commerce",
			OwnerTeam:         "commerce",
			FreshnessTargetMS: 15000,
			Accuracy:          "exact",
			Financial:         true,
			Description:       "Latest-state customer invoices with exact minor-unit billed and paid amounts.",
			Metrics: []MetricSpec{
				{
					Metric:            "commerce.invoices_paid_minor",
					Domain:            "commerce",
					Type:              "currency",
					Unit:              "minor_usd",
					Aggregation:       "sum",
					Dimensions:        []string{"org", "currency", "status"},
					ServingTable:      "analytics.snap_org_health",
					FreshnessTargetMS: 15000,
					Accuracy:          "exact",
					Description:       "Total collected invoice amount in minor currency units.",
				},
			},
		}),
	); err != nil {
		return nil, err
	}

	// 6. CommerceLedgerEntry (fact_commerce_ledger + L1 rollup_commerce_daily)
	if _, err := RegisterStruct[CommerceLedgerEntry](reg,
		WithKind(KindFact),
		WithSQL("commerce", "commerce_ledger_entries"),
		WithDatastore("analytics", "fact_commerce_ledger"),
		WithCatalog(CatalogMeta{
			Domain:            "finance",
			OwnerTeam:         "commerce",
			Retention:         "7 YEAR",
			FreshnessTargetMS: 2000,
			Accuracy:          "exact",
			Financial:         true,
			Description:       "Immutable append-only signed double-entry financial flow ledger (HIP-1001 / HIP-1405).",
			Metrics: []MetricSpec{
				{
					Metric:            "finance.net_revenue_minor",
					Domain:            "finance",
					Type:              "currency",
					Unit:              "minor_usd",
					Aggregation:       "sum",
					Dimensions:        []string{"org", "product", "currency", "kind"},
					ServingTable:      "analytics.rollup_commerce_daily",
					FreshnessTargetMS: 2000,
					Accuracy:          "exact",
					Description:       "Signed net ledger flow in minor currency units accumulated incrementally on ingest.",
				},
			},
		}),
		WithRollups(RollupSpec{
			Name:       "rollup_commerce_daily",
			Database:   "analytics",
			Engine:     EngineSummingMergeTree,
			TimeSource: "event_ts",
			TimeTarget: "day",
			Bucket:     BucketDay,
			Dimensions: []RollupDim{
				{TargetCol: "org", Type: TypeLowCardString},
				{TargetCol: "product", Type: TypeLowCardString},
				{TargetCol: "currency", Type: TypeLowCardString},
				{TargetCol: "kind", Type: TypeLowCardString},
			},
			Measures: []RollupMeasure{
				{TargetCol: "net_amount_minor", ValueType: "Int64", StateExpr: "sum(toInt64(sign) * amount_minor)", Unit: "minor_usd"},
				{TargetCol: "net_cash_minor", ValueType: "Int64", StateExpr: "sum(toInt64(sign) * cash_minor)", Unit: "minor_usd"},
				{TargetCol: "net_credit_minor", ValueType: "Int64", StateExpr: "sum(toInt64(sign) * credit_minor)", Unit: "minor_usd"},
				{TargetCol: "entry_count", ValueType: "Int64", StateExpr: "sum(toInt64(sign))", Unit: "count"},
			},
			Description: "Exact daily signed monetary flow rollup fed exclusively from immutable fact_commerce_ledger.",
		}),
	); err != nil {
		return nil, err
	}

	// 7. AIUsage (hanzo.cloud_usage + L1 rollup_ai_hourly & rollup_ai_daily)
	if _, err := RegisterStruct[AIUsage](reg,
		WithKind(KindFact),
		WithSQL("public", "ai_usage"),
		WithDatastore("hanzo", "cloud_usage"),
		WithCatalog(CatalogMeta{
			Domain:            "ai",
			OwnerTeam:         "ai-gateway",
			Retention:         "2 YEAR",
			FreshnessTargetMS: 2000,
			Accuracy:          "exact",
			Financial:         true,
			Description:       "Immutable per-inference AI gateway spend, token, margin, and prompt-cache savings ledger in nano-USD.",
			Metrics: []MetricSpec{
				{
					Metric:            "ai.billed_nano",
					Domain:            "ai",
					Type:              "currency",
					Unit:              "nano_usd",
					Aggregation:       "sum",
					Dimensions:        []string{"org", "model", "provider"},
					ServingTable:      "analytics.rollup_ai_daily",
					FreshnessTargetMS: 2000,
					Accuracy:          "exact",
					Description:       "Total AI gateway billed spend in nano-USD.",
				},
				{
					Metric:            "ai.margin_nano",
					Domain:            "ai",
					Type:              "currency",
					Unit:              "nano_usd",
					Aggregation:       "sum",
					Dimensions:        []string{"org", "model", "provider"},
					ServingTable:      "analytics.rollup_ai_daily",
					FreshnessTargetMS: 2000,
					Accuracy:          "exact",
					Description:       "Exact AI gross margin (billed_nano - cost_nano) in nano-USD.",
				},
			},
		}),
		WithRollups(
			RollupSpec{
				Name:       "rollup_ai_hourly",
				Database:   "analytics",
				Engine:     EngineAggregatingMergeTree,
				TimeSource: "timestamp",
				TimeTarget: "hour",
				Bucket:     BucketHour,
				Dimensions: []RollupDim{
					{TargetCol: "org", Type: TypeLowCardString},
					{TargetCol: "model", Type: TypeLowCardString},
					{TargetCol: "provider", Type: TypeLowCardString},
				},
				Measures: []RollupMeasure{
					{TargetCol: "requests", AggFunc: "count", StateExpr: "countState()", Unit: "count"},
					{TargetCol: "tokens", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(toInt64(total_tokens))", Unit: "tokens"},
					{TargetCol: "prompt_tokens_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(toInt64(prompt_tokens))", Unit: "tokens"},
					{TargetCol: "completion_tokens_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(toInt64(completion_tokens))", Unit: "tokens"},
					{TargetCol: "cache_read_tokens_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(toInt64(cache_read_tokens))", Unit: "tokens"},
					{TargetCol: "cost_nano_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(cost_nano)", Unit: "nano_usd"},
					{TargetCol: "billed_nano_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(billed_nano)", Unit: "nano_usd"},
					{TargetCol: "margin_nano_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(margin_nano)", Unit: "nano_usd"},
					{TargetCol: "cache_saved_nano_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(cache_saved_nano)", Unit: "nano_usd"},
					{TargetCol: "errors", AggFunc: "sum", ValueType: "UInt64", StateExpr: "sumState(toUInt64(status = 'error'))", Unit: "count"},
					{TargetCol: "users", AggFunc: "uniq", ValueType: "String", StateExpr: "uniqState(user_id)", Unit: "count"},
				},
				TTL: "hour + INTERVAL 180 DAY",
			},
			RollupSpec{
				Name:       "rollup_ai_daily",
				Database:   "analytics",
				Engine:     EngineAggregatingMergeTree,
				TimeSource: "timestamp",
				TimeTarget: "day",
				Bucket:     BucketDay,
				Dimensions: []RollupDim{
					{TargetCol: "org", Type: TypeLowCardString},
					{TargetCol: "model", Type: TypeLowCardString},
					{TargetCol: "provider", Type: TypeLowCardString},
				},
				Measures: []RollupMeasure{
					{TargetCol: "requests", AggFunc: "count", StateExpr: "countState()", Unit: "count"},
					{TargetCol: "tokens", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(toInt64(total_tokens))", Unit: "tokens"},
					{TargetCol: "cost_nano_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(cost_nano)", Unit: "nano_usd"},
					{TargetCol: "billed_nano_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(billed_nano)", Unit: "nano_usd"},
					{TargetCol: "margin_nano_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(margin_nano)", Unit: "nano_usd"},
					{TargetCol: "cache_saved_nano_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(cache_saved_nano)", Unit: "nano_usd"},
					{TargetCol: "errors", AggFunc: "sum", ValueType: "UInt64", StateExpr: "sumState(toUInt64(status = 'error'))", Unit: "count"},
					{TargetCol: "users", AggFunc: "uniq", ValueType: "String", StateExpr: "uniqState(user_id)", Unit: "count"},
				},
			},
		),
	); err != nil {
		return nil, err
	}

	// 8. ComputeSample (hanzo.compute_samples + L1 rollup_compute_hourly)
	if _, err := RegisterStruct[ComputeSample](reg,
		WithKind(KindFact),
		WithSQL("public", "compute_samples"),
		WithDatastore("hanzo", "compute_samples"),
		WithCatalog(CatalogMeta{
			Domain:            "compute",
			OwnerTeam:         "compute",
			Retention:         "90 DAY",
			FreshnessTargetMS: 5000,
			Accuracy:          "exact",
			Financial:         true,
			Description:       "Immutable GPU, CPU, memory, and sandbox compute metering samples.",
			Metrics: []MetricSpec{
				{
					Metric:            "compute.cost_nano",
					Domain:            "compute",
					Type:              "currency",
					Unit:              "nano_usd",
					Aggregation:       "sum",
					Dimensions:        []string{"org", "cluster", "region", "gpu_model", "tier"},
					ServingTable:      "analytics.rollup_compute_hourly",
					FreshnessTargetMS: 5000,
					Accuracy:          "exact",
					Description:       "Metered compute fleet cost in nano-USD.",
				},
			},
		}),
		WithRollups(RollupSpec{
			Name:       "rollup_compute_hourly",
			Database:   "analytics",
			Engine:     EngineAggregatingMergeTree,
			TimeSource: "timestamp",
			TimeTarget: "hour",
			Bucket:     BucketHour,
			Dimensions: []RollupDim{
				{TargetCol: "org", Type: TypeLowCardString},
				{TargetCol: "region", Type: TypeLowCardString},
				{TargetCol: "gpu_model", Type: TypeLowCardString},
				{TargetCol: "tier", Type: TypeLowCardString},
			},
			Measures: []RollupMeasure{
				{TargetCol: "samples", AggFunc: "count", StateExpr: "countState()", Unit: "count"},
				{TargetCol: "active_nodes", AggFunc: "uniq", ValueType: "String", StateExpr: "uniqState(node_id)", Unit: "count"},
				{TargetCol: "gpu_ms_sum", AggFunc: "sum", ValueType: "UInt64", StateExpr: "sumState(toUInt64(gpu_count) * toUInt64(duration_ms))", Unit: "ms"},
				{TargetCol: "mem_bytes_sum", AggFunc: "sum", ValueType: "UInt64", StateExpr: "sumState(mem_bytes)", Unit: "bytes"},
				{TargetCol: "cost_nano_sum", AggFunc: "sum", ValueType: "Int64", StateExpr: "sumState(cost_nano)", Unit: "nano_usd"},
			},
			TTL: "hour + INTERVAL 365 DAY",
		}),
	); err != nil {
		return nil, err
	}

	// 9. ProductEvent (event.fact + L1 rollup_product_hourly)
	if _, err := RegisterStruct[ProductEvent](reg,
		WithKind(KindFact),
		WithSQL("public", "product_events"),
		WithDatastore("event", "fact"),
		WithCatalog(CatalogMeta{
			Domain:            "product",
			OwnerTeam:         "insights",
			Retention:         "365 DAY",
			FreshnessTargetMS: 2000,
			Accuracy:          "approximate_hll",
			Description:       "Immutable product & behavioral analytics events (HIP-1190) aggregated incrementally via HLL sketches.",
			Metrics: []MetricSpec{
				{
					Metric:            "product.events",
					Domain:            "product",
					Type:              "counter",
					Unit:              "count",
					Aggregation:       "count",
					Dimensions:        []string{"org", "product", "name"},
					ServingTable:      "analytics.rollup_product_hourly",
					FreshnessTargetMS: 2000,
					Accuracy:          "exact",
					Description:       "Total product events ingested across all Hanzo surfaces.",
				},
				{
					Metric:            "product.active_users",
					Domain:            "product",
					Type:              "gauge",
					Unit:              "count",
					Aggregation:       "uniq",
					Dimensions:        []string{"org", "product"},
					ServingTable:      "analytics.rollup_product_hourly",
					FreshnessTargetMS: 2000,
					Accuracy:          "approximate_hll",
					Description:       "Distinct active persons via mergeable HyperLogLog state.",
				},
			},
		}),
		WithRollups(RollupSpec{
			Name:       "rollup_product_hourly",
			Database:   "analytics",
			Engine:     EngineAggregatingMergeTree,
			TimeSource: "event_time",
			TimeTarget: "hour",
			Bucket:     BucketHour,
			Dimensions: []RollupDim{
				{TargetCol: "org", Type: TypeLowCardString},
				{TargetCol: "product", Type: TypeLowCardString},
				{TargetCol: "name", Type: TypeString},
			},
			Measures: []RollupMeasure{
				{TargetCol: "events", AggFunc: "count", StateExpr: "countState()", Unit: "count"},
				{TargetCol: "users", AggFunc: "uniq", ValueType: "String", StateExpr: "uniqState(person_id)", Unit: "count"},
				{TargetCol: "sessions", AggFunc: "uniq", ValueType: "String", StateExpr: "uniqState(session_id)", Unit: "count"},
				{TargetCol: "bytes_sum", AggFunc: "sum", ValueType: "UInt64", StateExpr: "sumState(bytes)", Unit: "bytes"},
			},
			TTL: "hour + INTERVAL 365 DAY",
		}),
	); err != nil {
		return nil, err
	}

	// 10. O11ySpan (event.span + L1 rollup_o11y_hourly)
	if _, err := RegisterStruct[O11ySpan](reg,
		WithKind(KindFact),
		WithSQL("public", "o11y_spans"),
		WithDatastore("event", "span"),
		WithCatalog(CatalogMeta{
			Domain:            "infrastructure",
			OwnerTeam:         "o11y",
			Retention:         "30 DAY",
			FreshnessTargetMS: 2000,
			Accuracy:          "approximate_tdigest",
			Description:       "Immutable distributed tracing spans (HIP-0132 / HIP-1240) with mergeable t-digest latency quantiles.",
			Metrics: []MetricSpec{
				{
					Metric:            "o11y.span_p95_ns",
					Domain:            "infrastructure",
					Type:              "latency",
					Unit:              "ns",
					Aggregation:       "quantileTDigest(0.95)",
					Dimensions:        []string{"org", "service"},
					ServingTable:      "analytics.rollup_o11y_hourly",
					FreshnessTargetMS: 2000,
					Accuracy:          "approximate_tdigest",
					Description:       "95th percentile distributed span duration in nanoseconds.",
				},
			},
		}),
		WithRollups(RollupSpec{
			Name:       "rollup_o11y_hourly",
			Database:   "analytics",
			Engine:     EngineAggregatingMergeTree,
			TimeSource: "span_time",
			TimeTarget: "hour",
			Bucket:     BucketHour,
			Dimensions: []RollupDim{
				{TargetCol: "org", Type: TypeLowCardString},
				{TargetCol: "service", Type: TypeLowCardString},
			},
			Measures: []RollupMeasure{
				{TargetCol: "spans", AggFunc: "count", StateExpr: "countState()", Unit: "count"},
				{TargetCol: "errors", AggFunc: "sum", ValueType: "UInt64", StateExpr: "sumState(toUInt64(status = 'error'))", Unit: "count"},
				{TargetCol: "p50_ns", AggFunc: "quantileTDigest(0.50)", ValueType: "UInt64", StateExpr: "quantileTDigestState(0.50)(duration_ns)", Unit: "ns"},
				{TargetCol: "p95_ns", AggFunc: "quantileTDigest(0.95)", ValueType: "UInt64", StateExpr: "quantileTDigestState(0.95)(duration_ns)", Unit: "ns"},
				{TargetCol: "p99_ns", AggFunc: "quantileTDigest(0.99)", ValueType: "UInt64", StateExpr: "quantileTDigestState(0.99)(duration_ns)", Unit: "ns"},
			},
			TTL: "hour + INTERVAL 90 DAY",
		}),
	); err != nil {
		return nil, err
	}

	// L3 Serving Snapshot 1: analytics.snap_global_overview (ReplacingMergeTree(refreshed_at))
	// Zero raw-table scans, zero ClickHouse 26.2 alias shadowing.
	if err := reg.RegisterSnapshot(SnapshotSpec{
		Name:              "snap_global_overview",
		Database:          "analytics",
		Domain:            "commerce",
		FreshnessTargetMS: 2000,
		Description:       "L3 pre-materialized global executive KPI snapshot (O(1) single-row read).",
		Fields: []Field{
			{Name: "scope", Type: TypeLowCardString},
			{Name: "orgs_total", Type: TypeUint64},
			{Name: "customer_orgs", Type: TypeUint64},
			{Name: "members_total", Type: TypeUint64},
			{Name: "cash_minor", Type: TypeMoneyMinor},
			{Name: "credit_minor", Type: TypeMoneyMinor},
			{Name: "owed_minor", Type: TypeMoneyMinor},
			{Name: "active_subscriptions", Type: TypeUint64},
			{Name: "mrr_minor", Type: TypeMoneyMinor},
			{Name: "ai_requests_30d", Type: TypeUint64},
			{Name: "ai_billed_nano_30d", Type: TypeMoneyNano},
			{Name: "ai_margin_nano_30d", Type: TypeMoneyNano},
			{Name: "spans_24h", Type: TypeUint64},
			{Name: "errors_24h", Type: TypeUint64},
		},
		OrderBy: []string{"scope"},
		RefreshSelectSQL: `SELECT
  'global' AS scope,
  o.orgs_total,
  o.customer_orgs,
  o.members_total,
  b.total_cash_minor AS cash_minor,
  b.total_credit_minor AS credit_minor,
  b.total_owed_minor AS owed_minor,
  s.active_subscriptions,
  s.total_mrr_minor AS mrr_minor,
  a.ai_requests_30d,
  a.ai_billed_nano_30d,
  a.ai_margin_nano_30d,
  h.spans_24h,
  h.errors_24h,
  now64(3) AS refreshed_at
FROM (
  SELECT
    count() AS orgs_total,
    countIf(latest_is_internal = 0) AS customer_orgs,
    sum(latest_members) AS members_total
  FROM (
    SELECT
      org,
      argMax(is_internal, updated_at) AS latest_is_internal,
      argMax(members, updated_at) AS latest_members
    FROM analytics.dim_orgs
    GROUP BY org
  )
) AS o
CROSS JOIN (
  SELECT
    sum(latest_cash_minor) AS total_cash_minor,
    sum(latest_credit_minor) AS total_credit_minor,
    sum(latest_owed_minor) AS total_owed_minor
  FROM (
    SELECT
      org,
      currency,
      argMax(cash_minor, version) AS latest_cash_minor,
      argMax(credit_minor, version) AS latest_credit_minor,
      argMax(owed_minor, version) AS latest_owed_minor
    FROM analytics.state_org_balances
    GROUP BY org, currency
  )
) AS b
CROSS JOIN (
  SELECT
    countIf(latest_deleted = 0 AND latest_test = 0 AND latest_status IN ('active', 'trialing')) AS active_subscriptions,
    sumIf(latest_mrr_minor, latest_deleted = 0 AND latest_test = 0 AND latest_status = 'active') AS total_mrr_minor
  FROM (
    SELECT
      org,
      id,
      argMax(deleted, version) AS latest_deleted,
      argMax(test, version) AS latest_test,
      argMax(status, version) AS latest_status,
      argMax(mrr_minor, version) AS latest_mrr_minor
    FROM analytics.state_subscriptions
    GROUP BY org, id
  )
) AS s
CROSS JOIN (
  SELECT
    countMerge(requests) AS ai_requests_30d,
    sumMerge(billed_nano_sum) AS ai_billed_nano_30d,
    sumMerge(margin_nano_sum) AS ai_margin_nano_30d
  FROM analytics.rollup_ai_daily
  WHERE day >= today() - 30
) AS a
CROSS JOIN (
  SELECT
    countMerge(spans) AS spans_24h,
    sumMerge(errors) AS errors_24h
  FROM analytics.rollup_o11y_hourly
  WHERE hour >= now() - INTERVAL 24 HOUR
) AS h`,
	}); err != nil {
		return nil, err
	}

	// L3 Serving Snapshot 2: analytics.snap_org_health (ReplacingMergeTree(refreshed_at))
	if err := reg.RegisterSnapshot(SnapshotSpec{
		Name:              "snap_org_health",
		Database:          "analytics",
		Domain:            "cloud",
		FreshnessTargetMS: 5000,
		Description:       "L3 per-organization health & economics snapshot keyed by tenant org.",
		Fields: []Field{
			{Name: "org", Type: TypeLowCardString},
			{Name: "tier", Type: TypeLowCardString},
			{Name: "cash_minor", Type: TypeMoneyMinor},
			{Name: "credit_minor", Type: TypeMoneyMinor},
			{Name: "mrr_minor", Type: TypeMoneyMinor},
			{Name: "ai_billed_nano_30d", Type: TypeMoneyNano},
			{Name: "compute_cost_nano_30d", Type: TypeMoneyNano},
			{Name: "o11y_errors_24h", Type: TypeUint64},
		},
		OrderBy: []string{"org"},
		RefreshSelectSQL: `SELECT
  o.org,
  o.latest_tier AS tier,
  coalesce(b.org_cash_minor, 0) AS cash_minor,
  coalesce(b.org_credit_minor, 0) AS credit_minor,
  coalesce(s.org_mrr_minor, 0) AS mrr_minor,
  coalesce(a.org_ai_billed_nano, 0) AS ai_billed_nano_30d,
  coalesce(c.org_compute_cost_nano, 0) AS compute_cost_nano_30d,
  coalesce(h.org_errors_24h, 0) AS o11y_errors_24h,
  now64(3) AS refreshed_at
FROM (
  SELECT
    org,
    argMax(tier, updated_at) AS latest_tier
  FROM analytics.dim_orgs
  GROUP BY org
) AS o
LEFT JOIN (
  SELECT
    org,
    sum(latest_cash_minor) AS org_cash_minor,
    sum(latest_credit_minor) AS org_credit_minor
  FROM (
    SELECT
      org,
      currency,
      argMax(cash_minor, version) AS latest_cash_minor,
      argMax(credit_minor, version) AS latest_credit_minor
    FROM analytics.state_org_balances
    GROUP BY org, currency
  )
  GROUP BY org
) AS b USING (org)
LEFT JOIN (
  SELECT
    org,
    sumIf(latest_mrr_minor, latest_deleted = 0 AND latest_test = 0 AND latest_status = 'active') AS org_mrr_minor
  FROM (
    SELECT
      org,
      id,
      argMax(deleted, version) AS latest_deleted,
      argMax(test, version) AS latest_test,
      argMax(status, version) AS latest_status,
      argMax(mrr_minor, version) AS latest_mrr_minor
    FROM analytics.state_subscriptions
    GROUP BY org, id
  )
  GROUP BY org
) AS s USING (org)
LEFT JOIN (
  SELECT
    org,
    sumMerge(billed_nano_sum) AS org_ai_billed_nano
  FROM analytics.rollup_ai_daily
  WHERE day >= today() - 30
  GROUP BY org
) AS a USING (org)
LEFT JOIN (
  SELECT
    org,
    sumMerge(cost_nano_sum) AS org_compute_cost_nano
  FROM analytics.rollup_compute_hourly
  WHERE hour >= now() - INTERVAL 30 DAY
  GROUP BY org
) AS c USING (org)
LEFT JOIN (
  SELECT
    org,
    sumMerge(errors) AS org_errors_24h
  FROM analytics.rollup_o11y_hourly
  WHERE hour >= now() - INTERVAL 24 HOUR
  GROUP BY org
) AS h USING (org)`,
	}); err != nil {
		return nil, err
	}

	if err := reg.Validate(); err != nil {
		return nil, err
	}
	return reg, nil
}

// MustGlobalRegistry returns the canonical Hanzo global schema registry or
// panics if any schema invariant fails.
func MustGlobalRegistry() *Registry {
	reg, err := GlobalRegistry()
	if err != nil {
		panic(err)
	}
	return reg
}
