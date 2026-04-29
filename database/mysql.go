package database

import (
	"fmt"
	"log"

	"ai-case-be/config"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var DB *gorm.DB

// openMySQL 根据给定库名和连接配置建立 Gorm 数据库连接。
//
// 这里统一负责拼接 DSN、初始化驱动并在启动阶段快速失败，
// 这样上层只需要关心“连哪个业务库”，不需要重复处理连接细节。
func openMySQL(name string, cfg config.MySQLConfig) *gorm.DB {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.Username,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.DBName,
	)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect %s database: %v", name, err)
	}
	log.Printf("Connected to %s MySQL successfully", name)
	return db
}

// Connect 初始化当前服务默认使用的数据库连接并缓存到全局 DB。
//
// 当前项目只维护一个主业务库，因此这里直接把连接结果写入包级变量，
// 供 handlers、database 子模块和 AI 工具链路复用。
func Connect(cfg config.Config) {
	DB = openMySQL("gitlab_data", cfg.MySQL)
}
