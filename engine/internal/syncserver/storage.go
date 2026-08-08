package syncserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

const (
	StorageSQLite = "sqlite"
	StorageMySQL  = "mysql"
)

// StoreOptions contains native-only database credentials. Callers must never
// serialize this value into a frontend response or a log line.
type StoreOptions struct {
	Driver        string
	SQLitePath    string
	MySQLHost     string
	MySQLPort     string
	MySQLDatabase string
	MySQLUser     string
	MySQLPassword string
}

// OpenStore keeps the original SQLite entry point used by existing installs
// and tests. New deployments can opt into MySQL through OpenStoreWithOptions.
func OpenStore(path string) (*Store, error) {
	return OpenStoreWithOptions(StoreOptions{Driver: StorageSQLite, SQLitePath: path})
}

func OpenStoreWithOptions(options StoreOptions) (*Store, error) {
	driver := strings.ToLower(strings.TrimSpace(options.Driver))
	if driver == "" {
		driver = StorageSQLite
	}
	var (
		db      *sql.DB
		err     error
		dbLabel string
	)
	switch driver {
	case StorageSQLite:
		path := strings.TrimSpace(options.SQLitePath)
		if path == "" {
			return nil, errors.New("同步数据库路径为空")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("创建同步数据目录失败: %w", err)
		}
		db, err = sql.Open("sqlite", path)
		dbLabel = "SQLite"
		if db != nil {
			db.SetMaxOpenConns(1)
		}
	case StorageMySQL:
		host := strings.TrimSpace(options.MySQLHost)
		if host == "" {
			host = "127.0.0.1"
		}
		port := strings.TrimSpace(options.MySQLPort)
		if port == "" {
			port = "3306"
		}
		database := strings.TrimSpace(options.MySQLDatabase)
		user := strings.TrimSpace(options.MySQLUser)
		if database == "" || user == "" {
			return nil, errors.New("MySQL 数据库名和账号不能为空")
		}
		config := mysql.NewConfig()
		config.User = user
		config.Passwd = options.MySQLPassword
		config.Net = "tcp"
		config.Addr = host + ":" + port
		config.DBName = database
		config.Collation = "utf8mb4_bin"
		config.Timeout = 10 * time.Second
		config.ReadTimeout = 20 * time.Second
		config.WriteTimeout = 20 * time.Second
		config.Params = map[string]string{"charset": "utf8mb4"}
		db, err = sql.Open("mysql", config.FormatDSN())
		dbLabel = "MySQL"
		if db != nil {
			db.SetMaxOpenConns(12)
			db.SetMaxIdleConns(4)
			db.SetConnMaxLifetime(3 * time.Minute)
		}
	default:
		return nil, fmt.Errorf("不支持的同步存储类型: %s", driver)
	}
	if err != nil {
		return nil, fmt.Errorf("打开 %s 同步数据库失败: %w", dbLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接 %s 同步数据库失败: %w", dbLabel, err)
	}
	store := &Store{db: db, driver: driver, now: time.Now}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}
