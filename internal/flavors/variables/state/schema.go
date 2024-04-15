package state

import "github.com/hashicorp/go-memdb"

const (
	variableTableName = "variable"
)

var dbSchema = &memdb.DBSchema{
	Tables: map[string]*memdb.TableSchema{
		variableTableName: {
			Name: variableTableName,
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "path"},
				},
			},
		},
	},
}

func NewStateStore() (*VariableStore, error) {
	db, err := memdb.NewMemDB(dbSchema)
	if err != nil {
		return nil, err
	}

	return &VariableStore{
		db:        db,
		tableName: variableTableName,
		logger:    defaultLogger,
	}, nil
}
