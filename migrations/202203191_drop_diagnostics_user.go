package migrations

import (
	"github.com/emvi/logbuch"
	"github.com/muety/wakapi/config"
	"github.com/muety/wakapi/models"
	"gorm.io/gorm"
)

func init() {
	const name = "202203191-drop_diagnostics_user"
	f := migrationFunc{
		name: name,
		f: func(db *gorm.DB, cfg *config.Config) error {
			if hasRun(name, db) {
				return nil
			}

			migrator := db.Migrator()

			if migrator.HasColumn(&models.Diagnostics{}, "user_id") {
				logbuch.Info("running migration '%s'", name)

				if err := migrator.DropConstraint(&models.Diagnostics{}, "fk_diagnostics_user"); err != nil {
					logbuch.Warn("failed to drop 'fk_diagnostics_user' constraint (%v)", err)
				}

				if err := migrator.DropColumn(&models.Diagnostics{}, "user_id"); err != nil {
					logbuch.Warn("failed to drop user_id column of diagnostics (%v)", err)
				}
			}

			setHasRun(name, db)
			return nil
		},
	}

	registerPostMigration(f)
}
