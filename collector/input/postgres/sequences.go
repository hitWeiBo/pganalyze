package postgres

import (
	"database/sql"
	"fmt"

	"github.com/gedex/inflector"
	"github.com/pganalyze/collector/state"
	"github.com/pganalyze/collector/util"
)

const sequenceListSQL = `
SELECT c.oid, n.nspname, c.relname
 FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON (c.relnamespace = n.oid)
WHERE relkind = 'S' AND relpersistence = 'p'
`

const sequenceStateSQL = `
SELECT last_value, start_value, increment_by, max_value, min_value, cache_size, cycle
	FROM pganalyze.get_sequence_state($1, $2)
`

const serialColumnSQL = `
SELECT c.oid,
			 n.nspname,
			 c.relname,
			 a.attname,
			 pg_catalog.format_type(t.oid, a.atttypmod),
			 (pg_catalog.power(2, typlen * 8) / 2)::numeric,
			 pganalyze.get_sequence_oid_for_column(ad.adrelid::regclass::text, a.attname) AS sequence_oid
	FROM pg_catalog.pg_attrdef ad
	JOIN pg_catalog.pg_class c ON (c.oid = adrelid)
	JOIN pg_catalog.pg_namespace n ON (c.relnamespace = n.oid)
	JOIN pg_catalog.pg_attribute a ON (ad.adrelid = a.attrelid AND ad.adnum = a.attnum)
	JOIN pg_catalog.pg_type t ON (a.atttypid = t.oid)
 WHERE pg_catalog.pg_get_expr(adbin, adrelid) LIKE 'nextval%'
			 AND relkind = 'r' AND NOT attisdropped
`

const inferredForeignColumnSQL = `
SELECT c.oid,
			 n.nspname,
			 c.relname,
			 pg_catalog.format_type(t.oid, a.atttypmod),
			 (pg_catalog.power(2, typlen * 8) / 2)::numeric
	FROM pg_catalog.pg_attribute a
	JOIN pg_catalog.pg_class c ON (c.oid = a.attrelid)
	JOIN pg_catalog.pg_namespace n ON (c.relnamespace = n.oid)
	JOIN pg_catalog.pg_type t ON (a.atttypid = t.oid)
 WHERE attname = $1 AND relkind = 'r'
			 AND pg_catalog.format_type(t.oid, a.atttypmod) IN ('bigint', 'integer')
			 AND NOT attisdropped
`

func GetSequenceReport(logger *util.Logger, db *sql.DB) (report state.PostgresSequenceReport, err error) {
	report.Sequences = make(state.PostgresSequenceInformationMap)

	rows, err := db.Query(QueryMarkerSQL + sequenceListSQL)
	if err != nil {
		err = fmt.Errorf("SequenceReport/Query: %s", err)
		return
	}

	defer rows.Close()

	for rows.Next() {
		var oid state.Oid
		var seq state.PostgresSequenceInformation

		err = rows.Scan(&oid, &seq.SchemaName, &seq.SequenceName)
		if err != nil {
			err = fmt.Errorf("SequenceReport/Scan: %s", err)
			return
		}

		report.Sequences[oid] = seq
	}

	for oid, seq := range report.Sequences {
		err = db.QueryRow(QueryMarkerSQL+sequenceStateSQL, seq.SchemaName, seq.SequenceName).Scan(
			&seq.LastValue, &seq.StartValue, &seq.IncrementBy, &seq.MaxValue, &seq.MinValue, &seq.CacheValue, &seq.IsCycled)
		if err != nil {
			err = fmt.Errorf("SequenceReport/Sequence/Query: %s", err)
			return
		}
		report.Sequences[oid] = seq
	}

	columnRows, err := db.Query(QueryMarkerSQL + serialColumnSQL)
	if err != nil {
		err = fmt.Errorf("SequenceReport/Column/Query: %s", err)
		return
	}

	defer columnRows.Close()

	for columnRows.Next() {
		var col state.PostgresSerialColumn

		err = columnRows.Scan(&col.RelationOid, &col.SchemaName, &col.RelationName, &col.ColumnName,
			&col.DataType, &col.MaximumValue, &col.SequenceOid)
		if err != nil {
			err = fmt.Errorf("SequenceReport/Column/Scan: %s", err)
			return
		}

		report.SerialColumns = append(report.SerialColumns, col)
	}

	// Inferred foreign serial columns
	for idx, col := range report.SerialColumns {
		fColName := fmt.Sprintf("%s_%s", inflector.Singularize(col.RelationName), col.ColumnName)

		foreignRows, qErr := db.Query(QueryMarkerSQL+inferredForeignColumnSQL, fColName)
		if qErr != nil {
			err = fmt.Errorf("SequenceReport/InferForeign/Query: %s", qErr)
			return
		}

		defer foreignRows.Close()

		for foreignRows.Next() {
			fCol := state.PostgresForeignSerialColumn{
				ColumnName: fColName,
				Inferred:   true,
			}

			qErr := foreignRows.Scan(&fCol.RelationOid, &fCol.SchemaName,
				&fCol.RelationName, &fCol.DataType, &fCol.MaximumValue)
			if qErr != nil {
				err = fmt.Errorf("SequenceReport/InferForeign/Scan: %s", qErr)
				return
			}

			col.ForeignColumns = append(col.ForeignColumns, fCol)
		}

		report.SerialColumns[idx] = col
	}

	// TODO: Foreign column information from foreign keys

	report.DatabaseName, err = CurrentDatabaseName(db)
	if err != nil {
		return
	}

	return
}
