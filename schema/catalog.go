// Copyright © 2026 Hanzo AI. MIT License.

package schema

import (
	"encoding/json"
	"fmt"
	"slices"
)

// EntityGovernance captures governance, lineage, tenant isolation, PII, and
// retention metadata for one canonical Hanzo entity.
type EntityGovernance struct {
	Entity            string   `json:"entity"`
	Kind              string   `json:"kind"`
	Domain            string   `json:"domain"`
	OwnerTeam         string   `json:"owner_team,omitempty"`
	SQLTable          string   `json:"sql_table,omitempty"`
	DatastoreTable    string   `json:"datastore_table,omitempty"`
	TenantKey         string   `json:"tenant_key,omitempty"`
	TenantIsolated    bool     `json:"tenant_isolated"`
	RLS               bool     `json:"rls,omitempty"`
	Financial         bool     `json:"financial,omitempty"`
	PIIFields         []string `json:"pii_fields,omitempty"`
	Retention         string   `json:"retention,omitempty"`
	FreshnessTargetMS uint32   `json:"freshness_target_ms,omitempty"`
	Accuracy          string   `json:"accuracy,omitempty"`
	Rollups           []string `json:"rollups,omitempty"`
	Description       string   `json:"description,omitempty"`
}

// CatalogMetric mirrors one canonical row of `analytics.catalog` with tenant
// isolation and retention lineage attached.
type CatalogMetric struct {
	Metric            string   `json:"metric"`
	Domain            string   `json:"domain"`
	Type              string   `json:"type"`
	Unit              string   `json:"unit"`
	Source            string   `json:"source"`
	Aggregation       string   `json:"aggregation"`
	Dimensions        []string `json:"dimensions"`
	ServingTable      string   `json:"serving_table"`
	FreshnessTargetMS uint32   `json:"freshness_target_ms"`
	Accuracy          string   `json:"accuracy"`
	TenantKey         string   `json:"tenant_key,omitempty"`
	Retention         string   `json:"retention,omitempty"`
	Description       string   `json:"description"`
}

// SnapshotGovernance describes one L3 serving snapshot in the catalog.
type SnapshotGovernance struct {
	Table             string   `json:"table"`
	Domain            string   `json:"domain"`
	Engine            string   `json:"engine"`
	OrderBy           []string `json:"order_by"`
	FreshnessTargetMS uint32   `json:"freshness_target_ms"`
	Description       string   `json:"description,omitempty"`
}

// CatalogBundle is the complete Governance & Analytics Catalog compiled from a Registry.
type CatalogBundle struct {
	Namespace   string               `json:"namespace"`
	Fingerprint string               `json:"fingerprint"`
	Entities    []EntityGovernance   `json:"entities"`
	Metrics     []CatalogMetric      `json:"metrics"`
	Snapshots   []SnapshotGovernance `json:"snapshots,omitempty"`
}

// CompileCatalog extracts all governance metadata and canonical analytics
// catalog metric entries from r.
func (r *Registry) CompileCatalog() (CatalogBundle, error) {
	if err := r.Validate(); err != nil {
		return CatalogBundle{}, err
	}

	out := CatalogBundle{
		Namespace:   r.Namespace,
		Fingerprint: r.Fingerprint(),
	}

	for _, e := range r.entities {
		if !e.Planes.Has(PlaneCatalog) {
			continue
		}
		var pii []string
		for _, f := range e.Fields {
			if f.PII {
				pii = append(pii, f.Name)
			}
		}
		var rollups []string
		for _, rp := range e.Rollups {
			db := rp.Database
			if db == "" {
				db = "analytics"
			}
			rollups = append(rollups, db+"."+rp.Name)
		}

		gov := EntityGovernance{
			Entity:            e.Name,
			Kind:              string(e.Kind),
			Domain:            e.Catalog.Domain,
			OwnerTeam:         e.Catalog.OwnerTeam,
			TenantKey:         e.tenantColumn(),
			TenantIsolated:    e.Tenant.Isolated,
			RLS:               e.Tenant.RLS,
			Financial:         e.Catalog.Financial,
			PIIFields:         pii,
			Retention:         e.Catalog.Retention,
			FreshnessTargetMS: e.Catalog.FreshnessTargetMS,
			Accuracy:          e.Catalog.Accuracy,
			Rollups:           rollups,
			Description:       e.Catalog.Description,
		}
		if e.Planes.Has(PlaneSQL) {
			gov.SQLTable = e.FullSQLName()
		}
		if e.Planes.Has(PlaneDatastore) {
			gov.DatastoreTable = e.FullDatastoreName()
		}
		out.Entities = append(out.Entities, gov)

		for _, m := range e.Catalog.Metrics {
			domain := m.Domain
			if domain == "" {
				domain = e.Catalog.Domain
			}
			freshness := m.FreshnessTargetMS
			if freshness == 0 {
				freshness = e.Catalog.FreshnessTargetMS
			}
			acc := m.Accuracy
			if acc == "" {
				acc = e.Catalog.Accuracy
			}
			source := e.FullDatastoreName()
			if !e.Planes.Has(PlaneDatastore) {
				source = e.FullSQLName()
			}
			out.Metrics = append(out.Metrics, CatalogMetric{
				Metric:            m.Metric,
				Domain:            domain,
				Type:              m.Type,
				Unit:              m.Unit,
				Source:            source,
				Aggregation:       m.Aggregation,
				Dimensions:        slices.Clone(m.Dimensions),
				ServingTable:      m.ServingTable,
				FreshnessTargetMS: freshness,
				Accuracy:          acc,
				TenantKey:         e.tenantColumn(),
				Retention:         e.Catalog.Retention,
				Description:       m.Description,
			})
		}
	}

	for _, s := range r.snapshots {
		db := s.Database
		if db == "" {
			db = "analytics"
		}
		out.Snapshots = append(out.Snapshots, SnapshotGovernance{
			Table:             db + "." + s.Name,
			Domain:            s.Domain,
			Engine:            "ReplacingMergeTree(refreshed_at)",
			OrderBy:           slices.Clone(s.OrderBy),
			FreshnessTargetMS: s.FreshnessTargetMS,
			Description:       s.Description,
		})
	}

	return out, nil
}

// MarshalJSON renders the CatalogBundle as deterministic indented JSON.
func (c CatalogBundle) PrettyJSON() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

// EmitJSONSchema projects one Entity into an OpenAPI 3.1 / JSON Schema 2020-12
// object schema suitable for hanzoai/zip (`/.well-known/openapi.json`, `/v1/mcp`)
// and TypeScript/Python SDK codegen.
func EmitJSONSchema(e *Entity) (map[string]any, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	props := make(map[string]any, len(e.Fields))
	var required []string

	for _, f := range e.Fields {
		prop := fieldToJSONSchema(f)
		props[f.Name] = prop
		if !f.Nullable {
			required = append(required, f.Name)
		}
	}

	out := map[string]any{
		"type":                 "object",
		"title":                e.Name,
		"properties":           props,
		"additionalProperties": false,
		"x-hanzo-kind":         string(e.Kind),
		"x-hanzo-domain":       e.Catalog.Domain,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	if e.Catalog.Description != "" {
		out["description"] = e.Catalog.Description
	}
	if e.Tenant.Isolated {
		out["x-hanzo-tenant-key"] = e.tenantColumn()
	}
	if e.Planes.Has(PlaneSQL) {
		out["x-hanzo-sql-table"] = e.FullSQLName()
	}
	if e.Planes.Has(PlaneDatastore) {
		out["x-hanzo-datastore-table"] = e.FullDatastoreName()
	}
	return out, nil
}

// EmitOpenAPIComponents returns an OpenAPI 3.1 `components.schemas` map for
// every PlaneCatalog entity in r, ready to embed in zip's OpenAPI & MCP servers.
func (r *Registry) EmitOpenAPIComponents() (map[string]any, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	schemas := make(map[string]any, len(r.entities))
	for _, e := range r.entities {
		if !e.Planes.Has(PlaneCatalog) {
			continue
		}
		s, err := EmitJSONSchema(e)
		if err != nil {
			return nil, fmt.Errorf("schema: openapi %s: %w", e.Name, err)
		}
		schemas[e.Name] = s
	}
	return schemas, nil
}

func fieldToJSONSchema(f Field) map[string]any {
	prop := map[string]any{}
	switch f.Type {
	case TypeString, TypeLowCardString:
		prop["type"] = "string"
	case TypeBool:
		prop["type"] = "boolean"
	case TypeInt8, TypeInt16, TypeInt32, TypeUint8, TypeUint16, TypeUint32:
		prop["type"] = "integer"
		prop["format"] = "int32"
	case TypeInt64, TypeUint64, TypeMoneyMinor, TypeMoneyNano:
		prop["type"] = "integer"
		prop["format"] = "int64"
	case TypeFloat32:
		prop["type"] = "number"
		prop["format"] = "float"
	case TypeFloat64:
		prop["type"] = "number"
		prop["format"] = "double"
	case TypeTimestamp:
		prop["type"] = "string"
		prop["format"] = "date-time"
	case TypeDate:
		prop["type"] = "string"
		prop["format"] = "date"
	case TypeJSON:
		prop["type"] = "object"
	case TypeBytes:
		prop["type"] = "string"
		prop["format"] = "byte"
	case TypeStringArray:
		prop["type"] = "array"
		prop["items"] = map[string]any{"type": "string"}
	default:
		prop["type"] = "string"
	}
	if f.Unit != "" {
		prop["x-hanzo-unit"] = f.Unit
	}
	if f.PII {
		prop["x-hanzo-pii"] = true
	}
	if f.Comment != "" {
		prop["description"] = f.Comment
	}
	return prop
}
