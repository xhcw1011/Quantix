package data

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RunMigrations applies all *.sql files from migrationsDir in lexicographic order.
// Pass an empty string to use the default path "migrations" relative to the working directory.
// Files are expected to be idempotent (IF NOT EXISTS, ADD COLUMN IF NOT EXISTS, etc.).
func (s *Store) RunMigrations(ctx context.Context, migrationsDir string) error {
	if migrationsDir == "" {
		migrationsDir = "migrations"
	}

	pattern := filepath.Join(migrationsDir, "*.sql")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("list migrations in %s: %w", migrationsDir, err)
	}
	if len(files) == 0 {
		s.log.Sugar().Warnf("no SQL migration files found in %s", migrationsDir)
		return nil
	}
	sort.Strings(files) // 001_…, 002_…, … order

	for _, f := range files {
		sql, err := os.ReadFile(f) //nolint:gosec
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}
		if _, err := s.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", f, err)
		}
		s.log.Sugar().Infof("migration applied: %s", f)
	}
	return nil
}
