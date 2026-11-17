package model

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAutoMigrate_UserOIDCIdentity(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("FNS_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("FNS_TEST_MYSQL_DSN is unset")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("FNS_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("FNS_TEST_POSTGRES_DSN is unset")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			defer sqlDB.Close()
			require.NoError(t, AutoMigrate(db, "UserOIDCIdentity"))
			require.True(t, db.Migrator().HasTable(&UserOIDCIdentity{}))
			require.NoError(t, AutoMigrate(db, "UserOIDCIdentity"), "migration must be idempotent")
			identity := UserOIDCIdentity{UID: 1, Issuer: "https://issuer.example", Subject: "subject"}
			require.NoError(t, db.Create(&identity).Error)
			var loaded UserOIDCIdentity
			require.NoError(t, db.Where("issuer = ? AND subject = ?", identity.Issuer, identity.Subject).First(&loaded).Error)
			require.Equal(t, identity.UID, loaded.UID)
			duplicate := UserOIDCIdentity{UID: 2, Issuer: identity.Issuer, Subject: identity.Subject}
			require.Error(t, db.Create(&duplicate).Error, "issuer/subject must remain unique")
			require.NoError(t, db.Delete(&identity).Error)
			// Exercise the entire indexed value, including subjects differing only
			// at the last character (a prefix unique index would reject these).
			var longIDs []int64
			for _, suffix := range []string{"a", "b"} {
				long := UserOIDCIdentity{UID: 3, Issuer: "https://" + strings.Repeat("i", 504), Subject: strings.Repeat("s", 254) + suffix}
				require.NoError(t, db.Create(&long).Error)
				longIDs = append(longIDs, long.ID)
			}
			require.NoError(t, db.Delete(&UserOIDCIdentity{}, longIDs).Error)
		})
	}
}
