// Package migrations embeds the SQL migrations of every service so that the
// migrate command and the integration tests apply exactly the files that are
// committed to the repository.
package migrations

import "embed"

// FS holds the migration files. One directory per service database.
// A new service adds its directory here and to the //go:embed line.
//
//go:embed merchant/*.sql inventory/*.sql catalog/*.sql orders/*.sql
var FS embed.FS

// Directory names inside FS, one per owned database.
const (
	DirMerchant  = "merchant"
	DirInventory = "inventory"
	DirCatalog   = "catalog"
	DirOrders    = "orders"
)

// Known lists the migration directories that exist in this repository.
// It is used by the migrate command to validate its input.
var Known = []string{DirMerchant, DirInventory, DirCatalog, DirOrders}
