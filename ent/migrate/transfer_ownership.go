package migrate

import "entgo.io/ent/dialect/sql/schema"

// Ent models each relation with one column. Transfer metadata also persists a
// direct owner ID, so these effective migration tables add composite ownership
// constraints. Both database.Open and enttest migrate a copy of Tables.
func init() {
	addUniqueIndex(TailServersTable, "tailserver_user_id_id", "user_id", "id")
	addUniqueIndex(TailClientsTable, "tailclient_user_id_id", "user_id", "id")
	addUniqueIndex(TransferSharesTable, "transfershare_user_id_id", "user_id", "id")
	addUniqueIndex(TransferJobsTable, "transferjob_user_id_id", "user_id", "id")

	addOwnershipForeignKey(TransferSharesTable, TailServersTable, "transfer_shares_owner_server", "user_id", "server_id")
	addOwnershipForeignKey(ShareFilesTable, TransferSharesTable, "share_files_owner_share", "user_id", "share_id")
	addOwnershipForeignKey(TransferJobsTable, TailClientsTable, "transfer_jobs_owner_client", "user_id", "client_id")
	addOwnershipForeignKey(TransferItemsTable, TransferJobsTable, "transfer_items_owner_job", "user_id", "job_id")
}

func addUniqueIndex(table *schema.Table, name string, columns ...string) {
	table.Indexes = append(table.Indexes, &schema.Index{
		Name:    name,
		Unique:  true,
		Columns: transferColumns(table, columns...),
	})
}

func addOwnershipForeignKey(child, parent *schema.Table, symbol, ownerColumn, parentIDColumn string) {
	child.ForeignKeys = append(child.ForeignKeys, &schema.ForeignKey{
		Symbol:     symbol,
		Columns:    transferColumns(child, ownerColumn, parentIDColumn),
		RefTable:   parent,
		RefColumns: transferColumns(parent, "user_id", "id"),
		OnDelete:   schema.Cascade,
	})
}

func transferColumns(table *schema.Table, names ...string) []*schema.Column {
	columns := make([]*schema.Column, 0, len(names))
	for _, name := range names {
		for _, column := range table.Columns {
			if column.Name == name {
				columns = append(columns, column)
				break
			}
		}
	}
	return columns
}
