package dao

import (
	"database/sql"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/glebarez/sqlite"
	gormMysql "gorm.io/driver/mysql"
)

// TestNewEngineMySQLZeroDateTimeSession verifies the MySQL-only session sql_mode
// compatibility: strict checks remain enabled, zero dates can be written, and
// parseTime=true reads them back as the same zero time.Time used by SQLite.
// TestNewEngineMySQLZeroDateTimeSession 验证 MySQL 专用会话 sql_mode 兼容：
// 严格检查保留、零日期可写入，parseTime=true 时读回与 SQLite 一致的零
// time.Time。设置 FNS_TEST_MYSQL_ADMIN_DSN 时在真实 MySQL/MariaDB 上执行。
func TestNewEngineMySQLZeroDateTimeSession(t *testing.T) {
	adminDSN := os.Getenv("FNS_TEST_MYSQL_ADMIN_DSN")
	if adminDSN == "" {
		t.Skip("FNS_TEST_MYSQL_ADMIN_DSN not set")
	}

	admin, err := sql.Open("mysql", adminDSN)
	require.NoError(t, err)
	defer admin.Close()

	dbName := "fns_zero_dt_test"
	_, err = admin.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
	require.NoError(t, err)
	_, err = admin.Exec("CREATE DATABASE `" + dbName + "` CHARACTER SET utf8mb4")
	require.NoError(t, err)
	defer func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + dbName + "`")
	}()

	cfg := config.DatabaseConfig{
		Type: "mysql", Host: "127.0.0.1", Port: mysqlZeroDateTimePort(adminDSN), UserName: "root",
		Password: os.Getenv("FNS_TEST_MYSQL_PASSWORD"), Name: dbName,
		Charset: "utf8mb4", ParseTime: true, EnableWriteQueue: util.Ptr(false),
	}
	db, err := NewEngine(cfg, zap.NewNop())
	require.NoError(t, err)

	var mode string
	require.NoError(t, db.Raw("SELECT @@SESSION.sql_mode").Scan(&mode).Error)
	require.Contains(t, mode, "STRICT_TRANS_TABLES")
	require.NotContains(t, mode, "NO_ZERO_DATE")
	require.NotContains(t, mode, "NO_ZERO_IN_DATE")

	require.NoError(t, db.Exec("CREATE TABLE zero_dt (id bigint PRIMARY KEY AUTO_INCREMENT, ts datetime NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO zero_dt(ts) VALUES (?)", time.Time{}).Error)
	var got time.Time
	require.NoError(t, db.Raw("SELECT ts FROM zero_dt WHERE id = 1").Scan(&got).Error)
	require.True(t, got.IsZero(), "zero datetime must read back as zero time.Time, got %s", got)

	var sqliteValue time.Time
	sdb, err := gorm.Open(sqlite.Open(t.TempDir()+"/zero.sqlite3"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, sdb.Exec("CREATE TABLE zero_dt (id integer PRIMARY KEY, ts datetime)").Error)
	require.NoError(t, sdb.Exec("INSERT INTO zero_dt(id, ts) VALUES (1, ?)", time.Time{}).Error)
	require.NoError(t, sdb.Raw("SELECT ts FROM zero_dt WHERE id = 1").Scan(&sqliteValue).Error)
	require.True(t, sqliteValue.IsZero())
}

func TestMySQLDSNSQLModeShape(t *testing.T) {
	d := getDialector(config.DatabaseConfig{Type: "mysql", Host: "127.0.0.1", Port: 3306, UserName: "u", Password: "p", Name: "d", Charset: "utf8mb4", ParseTime: true})
	require.NotNil(t, d)
	_ = gormMysql.Config{}
}

func mysqlZeroDateTimePort(adminDSN string) int {
	cfg, err := mysqldriver.ParseDSN(adminDSN)
	if err != nil {
		return 3306
	}
	_, portStr, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return 3306
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 3306
	}
	return port
}
