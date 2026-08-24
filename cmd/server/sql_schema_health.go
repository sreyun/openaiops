package main

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"
)

// SchemaHealthFinding is a lightweight schema hygiene check result.
type SchemaHealthFinding struct {
	Level   string `json:"level"` // info|medium|high
	Code    string `json:"code"`
	Schema  string `json:"schema,omitempty"`
	Table   string `json:"table,omitempty"`
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Suggest string `json:"suggest,omitempty"`
}

func (s *Server) handleMySQLSchemaHealth(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cfg.GetMySQLConnection(strings.TrimSpace(r.PathValue("id")))
	if !ok || !c.Enabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "connection not found or disabled"})
		return
	}
	var (
		findings []SchemaHealthFinding
		err      error
	)
	if driverOf(c) == "postgres" {
		findings, err = pgSchemaHealth(c)
	} else {
		findings, err = mysqlSchemaHealth(c)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if findings == nil {
		findings = []SchemaHealthFinding{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connection_id": c.ID,
		"driver":        driverOf(c),
		"findings":      findings,
		"count":         len(findings),
	})
}

func mysqlSchemaHealth(c MySQLConnection) ([]SchemaHealthFinding, error) {
	db, err := sql.Open("mysql", mysqlDSNSlow(c))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	exclude := defaultSlowSQLExcludeSchemas()
	exSet := map[string]bool{}
	for _, s := range exclude {
		exSet[strings.ToLower(s)] = true
	}
	var out []SchemaHealthFinding

	// Tables without primary key
	qPK := `
SELECT t.table_schema, t.table_name
FROM information_schema.tables t
LEFT JOIN information_schema.table_constraints tc
  ON tc.table_schema = t.table_schema AND tc.table_name = t.table_name AND tc.constraint_type = 'PRIMARY KEY'
WHERE t.table_type = 'BASE TABLE' AND tc.constraint_name IS NULL
LIMIT 80`
	if rs, err := db.QueryContext(ctx, qPK); err == nil {
		for rs.Next() {
			var schema, table string
			if rs.Scan(&schema, &table) != nil {
				continue
			}
			if exSet[strings.ToLower(schema)] {
				continue
			}
			out = append(out, SchemaHealthFinding{
				Level: "medium", Code: "no_pk", Schema: schema, Table: table,
				Title: "表缺少主键", Detail: schema + "." + table,
				Suggest: "补充 PRIMARY KEY / 业务唯一键，降低复制与变更风险",
			})
		}
		noteRowsErr("mysqlSchemaHealth#1", rs)
		rs.Close()
	}

	// MyISAM tables (non-transactional)
	qEng := `
SELECT table_schema, table_name, engine
FROM information_schema.tables
WHERE table_type='BASE TABLE' AND UPPER(IFNULL(engine,'')) = 'MYISAM'
LIMIT 50`
	if rs, err := db.QueryContext(ctx, qEng); err == nil {
		for rs.Next() {
			var schema, table, eng string
			if rs.Scan(&schema, &table, &eng) != nil {
				continue
			}
			if exSet[strings.ToLower(schema)] {
				continue
			}
			out = append(out, SchemaHealthFinding{
				Level: "high", Code: "myisam", Schema: schema, Table: table,
				Title: "MyISAM 引擎表", Detail: schema + "." + table + " ENGINE=" + eng,
				Suggest: "评估迁移到 InnoDB 以获得事务与崩溃恢复",
			})
		}
		noteRowsErr("mysqlSchemaHealth#2", rs)
		rs.Close()
	}

	// Large tables without recent index (heuristic: data_length > 512MB and zero indexes)
	qIdx := `
SELECT t.table_schema, t.table_name, t.data_length, COUNT(s.index_name) AS idx_cnt
FROM information_schema.tables t
LEFT JOIN information_schema.statistics s
  ON s.table_schema = t.table_schema AND s.table_name = t.table_name
WHERE t.table_type='BASE TABLE' AND IFNULL(t.data_length,0) > 536870912
GROUP BY t.table_schema, t.table_name, t.data_length
HAVING idx_cnt = 0
LIMIT 40`
	if rs, err := db.QueryContext(ctx, qIdx); err == nil {
		for rs.Next() {
			var schema, table string
			var dataLen int64
			var idxCnt int
			if rs.Scan(&schema, &table, &dataLen, &idxCnt) != nil {
				continue
			}
			if exSet[strings.ToLower(schema)] {
				continue
			}
			out = append(out, SchemaHealthFinding{
				Level: "medium", Code: "large_no_index", Schema: schema, Table: table,
				Title: "大表无索引", Detail: schema + "." + table,
				Suggest: "核查查询路径并补充合适二级索引",
			})
		}
		noteRowsErr("mysqlSchemaHealth#3", rs)
		rs.Close()
	}

	return out, nil
}
