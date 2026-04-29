
CREATE TABLE IF NOT EXISTS ai_test_case_records (
id BIGINT NOT NULL AUTO_INCREMENT,
title VARCHAR(255) NOT NULL,
case_title VARCHAR(255) NOT NULL,
preconditions TEXT NULL,
steps TEXT NULL,
expected_result TEXT NULL,
priority VARCHAR(32) NOT NULL DEFAULT '中',
status VARCHAR(32) NOT NULL DEFAULT 'created',
source VARCHAR(32) NOT NULL DEFAULT 'ai',
provider VARCHAR(32) NOT NULL DEFAULT 'codex',
creator VARCHAR(64) NULL,
created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
PRIMARY KEY (id),
KEY idx_title (title),
KEY idx_case_title (case_title),
KEY idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;